package com.blink.shop.chat;

import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.widget.TextView;

import java.util.ArrayList;
import java.util.List;
import java.util.TimeZone;

import androidx.recyclerview.widget.RecyclerView;

import com.blink.shop.R;
import com.blink.shop.model.ChatSession;
import com.blink.shop.model.Times;

/** 历史抽屉里的会话列表。 */
public final class ChatHistoryAdapter extends RecyclerView.Adapter<ChatHistoryAdapter.Holder> {

    public interface Listener {
        void onOpen(ChatSession s);

        void onMore(ChatSession s);
    }

    private final List<ChatSession> items = new ArrayList<>();
    private final Listener listener;
    private String activeId = "";

    public ChatHistoryAdapter(Listener listener) {
        this.listener = listener;
    }

    public void submit(List<ChatSession> sessions, String activeSessionId) {
        items.clear();
        items.addAll(sessions);
        activeId = activeSessionId == null ? "" : activeSessionId;
        notifyDataSetChanged();
    }

    public void setActive(String sessionId) {
        activeId = sessionId == null ? "" : sessionId;
        notifyDataSetChanged();
    }

    @Override
    public int getItemCount() {
        return items.size();
    }

    @Override
    public Holder onCreateViewHolder(ViewGroup parent, int viewType) {
        return new Holder(LayoutInflater.from(parent.getContext()).inflate(R.layout.item_chat_session, parent, false));
    }

    @Override
    public void onBindViewHolder(Holder holder, int position) {
        holder.bind(items.get(position));
    }

    final class Holder extends RecyclerView.ViewHolder {
        final TextView title;
        final TextView summary;
        final TextView time;
        final TextView more;

        Holder(View v) {
            super(v);
            title = v.findViewById(R.id.session_title);
            summary = v.findViewById(R.id.session_summary);
            time = v.findViewById(R.id.session_time);
            more = v.findViewById(R.id.session_more);
        }

        void bind(ChatSession s) {
            boolean active = s.sessionId.equals(activeId);
            title.setText((s.pinned ? "📌 " : "") + s.displayTitle());
            title.setTextColor(itemView.getContext().getColor(active ? R.color.brand : R.color.text_primary));
            summary.setText(s.summary);
            summary.setVisibility(s.summary.isEmpty() ? View.GONE : View.VISIBLE);
            time.setText(s.lastMessageAt.isEmpty() ? "" : Times.formatLocal(s.lastMessageAt, TimeZone.getDefault()));
            itemView.setContentDescription((s.pinned ? "已置顶，" : "") + s.displayTitle() + (active ? "，当前会话" : ""));
            itemView.setOnClickListener(v -> listener.onOpen(s));
            itemView.setOnLongClickListener(v -> {
                listener.onMore(s);
                return true;
            });
            more.setOnClickListener(v -> listener.onMore(s));
            more.setContentDescription(s.displayTitle() + " 的更多操作");
        }
    }
}
