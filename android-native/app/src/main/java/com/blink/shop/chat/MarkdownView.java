package com.blink.shop.chat;

import android.content.Intent;
import android.graphics.Typeface;
import android.net.Uri;
import android.text.SpannableStringBuilder;
import android.text.Spanned;
import android.text.method.LinkMovementMethod;
import android.text.style.BulletSpan;
import android.text.style.LeadingMarginSpan;
import android.text.style.QuoteSpan;
import android.text.style.RelativeSizeSpan;
import android.text.style.StyleSpan;
import android.text.style.TypefaceSpan;
import android.text.style.URLSpan;
import android.view.View;
import android.widget.TextView;

import java.util.List;

import com.blink.shop.R;

/** 把 MarkdownLite 的块渲染到 TextView（Spannable）。链接只开 http(s)，用系统浏览器。 */
public final class MarkdownView {

    private MarkdownView() {
    }

    public static void apply(TextView view, String markdown) {
        List<MarkdownLite.Block> blocks = MarkdownLite.parse(markdown);
        SpannableStringBuilder sb = new SpannableStringBuilder();
        float d = view.getResources().getDisplayMetrics().density;
        boolean hasLink = false;
        for (int i = 0; i < blocks.size(); i++) {
            MarkdownLite.Block b = blocks.get(i);
            if (i > 0) {
                sb.append('\n');
                if (b.kind == MarkdownLite.Kind.PARAGRAPH || b.kind == MarkdownLite.Kind.HEADING || b.kind == MarkdownLite.Kind.CODE) {
                    sb.append('\n');
                }
            }
            int start = sb.length();
            switch (b.kind) {
                case CODE:
                    sb.append(b.code);
                    sb.setSpan(new TypefaceSpan("monospace"), start, sb.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
                    break;
                case NUMBERED:
                    sb.append(String.valueOf(b.number)).append(". ");
                    hasLink |= runs(sb, b.runs);
                    sb.setSpan(new LeadingMarginSpan.Standard(0, Math.round(18 * d)), start, sb.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
                    break;
                case BULLET:
                    hasLink |= runs(sb, b.runs);
                    sb.setSpan(new BulletSpan(Math.round(8 * d), view.getCurrentTextColor()), start, sb.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
                    break;
                case QUOTE:
                    hasLink |= runs(sb, b.runs);
                    sb.setSpan(new QuoteSpan(view.getContext().getColor(R.color.brand)), start, sb.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
                    break;
                case HEADING:
                    hasLink |= runs(sb, b.runs);
                    sb.setSpan(new StyleSpan(Typeface.BOLD), start, sb.length(), Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
                    sb.setSpan(new RelativeSizeSpan(b.level == 1 ? 1.25f : b.level == 2 ? 1.15f : 1.05f), start, sb.length(),
                            Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
                    break;
                default:
                    hasLink |= runs(sb, b.runs);
            }
        }
        view.setText(sb);
        view.setMovementMethod(hasLink ? LinkMovementMethod.getInstance() : null);
        view.setLinksClickable(hasLink);
    }

    private static boolean runs(SpannableStringBuilder sb, List<MarkdownLite.Run> runs) {
        boolean link = false;
        for (MarkdownLite.Run r : runs) {
            int start = sb.length();
            sb.append(r.text);
            int end = sb.length();
            if (r.bold && r.italic) {
                sb.setSpan(new StyleSpan(Typeface.BOLD_ITALIC), start, end, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
            } else if (r.bold) {
                sb.setSpan(new StyleSpan(Typeface.BOLD), start, end, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
            } else if (r.italic) {
                sb.setSpan(new StyleSpan(Typeface.ITALIC), start, end, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
            }
            if (r.code) {
                sb.setSpan(new TypefaceSpan("monospace"), start, end, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
            }
            if (!r.url.isEmpty()) {
                link = true;
                sb.setSpan(new URLSpan(r.url) {
                    @Override
                    public void onClick(View widget) {
                        try {
                            widget.getContext().startActivity(new Intent(Intent.ACTION_VIEW, Uri.parse(getURL())));
                        } catch (RuntimeException ignored) {
                            // 没有浏览器可用
                        }
                    }
                }, start, end, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE);
            }
        }
        return link;
    }
}
