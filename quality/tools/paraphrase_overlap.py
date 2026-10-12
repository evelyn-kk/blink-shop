#!/usr/bin/env python3
"""核对改写留出集与期望文档的字面重合：问题按与 rag.QueryTerms 相同的方式切词（汉字相邻二元组、字母数字整段、单个汉字），
统计在期望文档（标题 + 正文，小写）里出现的词占比。超过阈值的用例列出来并以退出码 1 结束。

    python3 quality/tools/paraphrase_overlap.py quality/data/eval/rag_paraphrase_holdout.jsonl
"""
import html
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
STOP_CHARS = set("吗呢吧的了么怎什哪啊呀嘛呗哦")


def is_han(ch):
    return "一" <= ch <= "鿿"


def terms(query):
    out, run, han = [], [], False

    def flush():
        if not run:
            return
        if not han:
            if len(run) >= 2 or run[0].isdigit():
                out.append("".join(run).lower())
        elif len(run) == 1:
            if run[0] not in STOP_CHARS:
                out.append(run[0])
        else:
            for i in range(len(run) - 1):
                if run[i] in STOP_CHARS or run[i + 1] in STOP_CHARS:
                    continue
                out.append(run[i] + run[i + 1])
        run.clear()

    for ch in query:
        h = is_han(ch)
        if not h and not ch.isalnum():
            flush()
            continue
        if run and h != han:
            flush()
        han = h
        run.append(ch)
    flush()
    seen, uniq = set(), []
    for t in out:
        if t not in seen:
            seen.add(t)
            uniq.append(t)
    return uniq


def corpus_texts():
    docs = json.loads((ROOT / "backend/fixtures/rag/corpus.json").read_text())["documents"]
    texts = {}
    for d in docs:
        body = d.get("content") or d.get("json_text") or re.sub(r"<[^>]+>", " ", html.unescape(d.get("html", "")))
        texts[d["key"]] = (d.get("title", "") + "\n" + body).lower()
    # 开发种子的 3 篇资料（backend/src/seed/seed.go）
    seed = (ROOT / "backend/src/seed/seed.go").read_text()
    for m in re.finditer(r'\{"(doc_seed_\w+)", "([^"]+)", "\w+", \[\]chunk\{(.*?)\n\t\t\}\}', seed, re.S):
        texts[m.group(1)] = (m.group(2) + "\n" + " ".join(re.findall(r'"([^"]*)"', m.group(3)))).lower()
    return texts


def main():
    path = sys.argv[1]
    limit = float(sys.argv[2]) if len(sys.argv) > 2 else 0.2
    texts = corpus_texts()
    bad = 0
    for line in Path(path).read_text().splitlines():
        if not line.strip() or line.startswith("#"):
            continue
        c = json.loads(line)
        ts = terms(c["query"])
        for want in c["expect"]:
            hit = [t for t in ts if t in texts[want]]
            ratio = len(hit) / len(ts) if ts else 0
            flag = "OVER" if ratio > limit else "ok"
            bad += ratio > limit
            print(f"{c['id']}\t{want}\t{ratio:.2f}\t{flag}\t{hit}")
    print(f"超过 {limit:.0%} 的用例：{bad}")
    sys.exit(1 if bad else 0)


if __name__ == "__main__":
    main()
