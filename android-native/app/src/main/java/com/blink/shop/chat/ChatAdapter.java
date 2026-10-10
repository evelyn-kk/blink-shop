package com.blink.shop.chat;

import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.util.ArrayList;
import java.util.List;

import androidx.recyclerview.widget.RecyclerView;

import com.blink.shop.R;
import com.blink.shop.ui.Chips;

/**
 * 聊天列表：每一轮一项（用户气泡 + 导购回答）。流式更新时只改动变化的部分：正文就地刷新，块只追加，
 * 步骤面板重建（很小），追问在结束后才显示。
 */
public final class ChatAdapter extends RecyclerView.Adapter<ChatAdapter.Holder> {

    public interface Listener {
        void onRetry(ChatTurn turn);

        void onResend(ChatTurn turn);

        void onFollowup(String question);
    }

    private final List<ChatTurn> turns = new ArrayList<>();
    private final BlockViews blocks;
    private final Listener listener;

    public ChatAdapter(BlockViews blocks, Listener listener) {
        this.blocks = blocks;
        this.listener = listener;
        setHasStableIds(true);
    }

    /** 用控制器的最新列表刷新；按 client_message_id 判断同一轮，只有修订号变了才重绑。 */
    public void submit(List<ChatTurn> latest) {
        List<ChatTurn> old = new ArrayList<>(turns);
        turns.clear();
        turns.addAll(latest);
        if (old.size() != latest.size()) {
            notifyDataSetChanged();
            return;
        }
        for (int i = 0; i < latest.size(); i++) {
            if (old.get(i) != latest.get(i)) {
                notifyItemChanged(i);
            } else {
                notifyItemChanged(i, latest.get(i).revision());
            }
        }
    }

    @Override
    public long getItemId(int position) {
        return turns.get(position).clientMessageId.hashCode();
    }

    @Override
    public int getItemCount() {
        return turns.size();
    }

    @Override
    public Holder onCreateViewHolder(ViewGroup parent, int viewType) {
        return new Holder(LayoutInflater.from(parent.getContext()).inflate(R.layout.item_chat_turn, parent, false));
    }

    @Override
    public void onBindViewHolder(Holder holder, int position) {
        holder.bind(turns.get(position), true);
    }

    @Override
    public void onBindViewHolder(Holder holder, int position, List<Object> payloads) {
        holder.bind(turns.get(position), payloads.isEmpty());
    }

    final class Holder extends RecyclerView.ViewHolder {
        final TextView userText;
        final TextView userAttachment;
        final TextView userTime;
        final LinearLayout stepsPanel;
        final TextView stepsSummary;
        final LinearLayout stepsList;
        final TextView answer;
        final LinearLayout blockList;
        final TextView status;
        final TextView retry;
        final LinearLayout followups;
        final View typing;

        ChatTurn bound;
        int boundRevision = -1;
        int renderedBlocks;
        boolean stepsExpanded;

        Holder(View v) {
            super(v);
            userText = v.findViewById(R.id.user_text);
            userAttachment = v.findViewById(R.id.user_attachment);
            userTime = v.findViewById(R.id.user_time);
            stepsPanel = v.findViewById(R.id.steps_panel);
            stepsSummary = v.findViewById(R.id.steps_summary);
            stepsList = v.findViewById(R.id.steps_list);
            answer = v.findViewById(R.id.answer_text);
            blockList = v.findViewById(R.id.block_list);
            status = v.findViewById(R.id.status_text);
            retry = v.findViewById(R.id.retry_button);
            followups = v.findViewById(R.id.followups);
            typing = v.findViewById(R.id.typing);
            stepsSummary.setOnClickListener(x -> {
                stepsExpanded = !stepsExpanded;
                stepsList.setVisibility(stepsExpanded ? View.VISIBLE : View.GONE);
                stepsSummary.setContentDescription((stepsExpanded ? "收起" : "展开") + "处理步骤");
            });
        }

        void bind(ChatTurn t, boolean full) {
            boolean same = bound == t && !full;
            if (same && boundRevision == t.revision()) {
                return;
            }
            if (!same) {
                userText.setText(t.userText);
                int images = t.imageCount();
                userAttachment.setVisibility(images > 0 ? View.VISIBLE : View.GONE);
                userAttachment.setText(images > 0 ? "附带 " + images + " 张图片" : "");
                userTime.setText(t.createdAt.isEmpty() ? "" : com.blink.shop.model.Times.formatLocal(t.createdAt, java.util.TimeZone.getDefault()));
                userTime.setVisibility(t.createdAt.isEmpty() ? View.GONE : View.VISIBLE);
                blockList.removeAllViews();
                renderedBlocks = 0;
                stepsExpanded = false;
                stepsList.setVisibility(View.GONE);
            }
            bound = t;
            boundRevision = t.revision();
            // 步骤
            List<ChatTurn.Step> steps = t.steps();
            if (steps.isEmpty()) {
                stepsPanel.setVisibility(View.GONE);
            } else {
                stepsPanel.setVisibility(View.VISIBLE);
                ChatTurn.Step last = steps.get(steps.size() - 1);
                int done = 0;
                for (ChatTurn.Step s : steps) {
                    if (s.done) {
                        done++;
                    }
                }
                boolean running = t.isActive() && !last.done;
                stepsSummary.setText((running ? "⟳ " : "✓ ") + (running ? last.title : done + " 个步骤") + (stepsExpanded ? " ▴" : " ▾"));
                stepsList.removeAllViews();
                for (ChatTurn.Step s : steps) {
                    TextView row = new TextView(itemView.getContext());
                    row.setText((s.done ? "✓ " : "⟳ ") + s.title);
                    row.setTextSize(12);
                    row.setTextColor(itemView.getContext().getColor(s.done ? R.color.text_secondary : R.color.brand));
                    row.setPadding(0, 2, 0, 2);
                    stepsList.addView(row);
                }
            }
            // 正文
            String text = t.text();
            if (text.isEmpty()) {
                answer.setVisibility(View.GONE);
            } else {
                answer.setVisibility(View.VISIBLE);
                MarkdownView.apply(answer, text);
            }
            // 块：只追加新的
            List<org.json.JSONObject> bs = t.blocks();
            for (int i = renderedBlocks; i < bs.size(); i++) {
                View card = blocks.build(bs.get(i));
                if (card != null) {
                    blockList.addView(card);
                }
            }
            renderedBlocks = bs.size();
            blockList.setVisibility(blockList.getChildCount() == 0 ? View.GONE : View.VISIBLE);
            // 状态行
            typing.setVisibility(t.isActive() && text.isEmpty() ? View.VISIBLE : View.GONE);
            retry.setVisibility(View.GONE);
            switch (t.status()) {
                case ERROR:
                    status.setVisibility(View.VISIBLE);
                    status.setText(t.errorMessage());
                    status.setTextColor(itemView.getContext().getColor(R.color.danger));
                    retry.setVisibility(View.VISIBLE);
                    retry.setText("重试");
                    retry.setOnClickListener(v -> listener.onRetry(t));
                    break;
                case CANCELLED:
                    status.setVisibility(View.VISIBLE);
                    status.setText(t.errorMessage());
                    status.setTextColor(itemView.getContext().getColor(R.color.text_hint));
                    retry.setVisibility(View.VISIBLE);
                    retry.setText("重新提问");
                    retry.setOnClickListener(v -> listener.onResend(t));
                    break;
                case SENDING:
                    status.setVisibility(View.VISIBLE);
                    status.setText(t.replayed() ? "正在取回回答…" : "正在思考…");
                    status.setTextColor(itemView.getContext().getColor(R.color.text_hint));
                    break;
                default:
                    status.setVisibility(View.GONE);
            }
            // 追问：结束后才显示
            followups.removeAllViews();
            boolean showFollowups = t.status() == ChatTurn.Status.DONE && !t.followups().isEmpty();
            followups.setVisibility(showFollowups ? View.VISIBLE : View.GONE);
            if (showFollowups) {
                for (String q : t.followups()) {
                    followups.addView(Chips.make(itemView.getContext(), q, false, v -> listener.onFollowup(q)));
                }
            }
        }
    }
}
