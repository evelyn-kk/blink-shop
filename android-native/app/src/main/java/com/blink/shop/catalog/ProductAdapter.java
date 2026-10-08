package com.blink.shop.catalog;

import android.graphics.Paint;
import android.text.TextUtils;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.widget.ImageView;
import android.widget.TextView;

import androidx.recyclerview.widget.RecyclerView;

import java.util.ArrayList;
import java.util.List;

import com.blink.shop.R;
import com.blink.shop.model.Money;
import com.blink.shop.model.Product;
import com.blink.shop.ui.ImageLoader;

/** 商品卡片列表，末尾一行显示翻页状态。 */
final class ProductAdapter extends RecyclerView.Adapter<RecyclerView.ViewHolder> {

    enum Footer { NONE, LOADING, ERROR, END }

    interface Listener {
        void onProductClick(Product p);

        void onRetryMore();

        void onAddToCart(Product p);
    }

    private static final int TYPE_ITEM = 0;
    private static final int TYPE_FOOTER = 1;

    private final List<Product> items = new ArrayList<>();
    private final Listener listener;
    private ImageLoader images;
    private Footer footer = Footer.NONE;
    private int imagePx;

    ProductAdapter(Listener listener) {
        this.listener = listener;
    }

    void setImages(ImageLoader images, int imagePx) {
        this.images = images;
        this.imagePx = imagePx;
    }

    void setItems(List<Product> list, Footer f) {
        items.clear();
        items.addAll(list);
        footer = f;
        notifyDataSetChanged();
    }

    @Override
    public int getItemCount() {
        return items.size() + (footer == Footer.NONE ? 0 : 1);
    }

    @Override
    public int getItemViewType(int position) {
        return position < items.size() ? TYPE_ITEM : TYPE_FOOTER;
    }

    @Override
    public RecyclerView.ViewHolder onCreateViewHolder(ViewGroup parent, int viewType) {
        LayoutInflater inf = LayoutInflater.from(parent.getContext());
        if (viewType == TYPE_FOOTER) {
            return new FooterHolder(inf.inflate(R.layout.item_list_footer, parent, false));
        }
        return new ItemHolder(inf.inflate(R.layout.item_product, parent, false));
    }

    @Override
    public void onBindViewHolder(RecyclerView.ViewHolder holder, int position) {
        if (holder instanceof FooterHolder) {
            ((FooterHolder) holder).bind(footer);
        } else {
            ((ItemHolder) holder).bind(items.get(position));
        }
    }

    final class ItemHolder extends RecyclerView.ViewHolder {
        final ImageView image;
        final TextView name;
        final TextView byline;
        final TextView points;
        final TextView price;
        final TextView marketPrice;
        final TextView stock;
        final TextView add;

        ItemHolder(View v) {
            super(v);
            image = v.findViewById(R.id.product_image);
            name = v.findViewById(R.id.product_name);
            byline = v.findViewById(R.id.product_byline);
            points = v.findViewById(R.id.product_points);
            price = v.findViewById(R.id.product_price);
            marketPrice = v.findViewById(R.id.product_market_price);
            stock = v.findViewById(R.id.product_stock);
            add = v.findViewById(R.id.product_add);
            marketPrice.setPaintFlags(marketPrice.getPaintFlags() | Paint.STRIKE_THRU_TEXT_FLAG);
        }

        void bind(Product p) {
            name.setText(p.name.isEmpty() ? "未命名商品" : p.name);
            byline.setText(p.byline());
            String pts = p.sellingPoints.isEmpty() ? p.recommendReason : TextUtils.join(" · ", p.sellingPoints);
            points.setText(pts);
            points.setVisibility(pts.isEmpty() ? View.GONE : View.VISIBLE);
            price.setText(Money.format(p.price));
            boolean showMarket = Money.greater(p.marketPrice, p.price);
            marketPrice.setText(showMarket ? Money.format(p.marketPrice) : "");
            marketPrice.setContentDescription(showMarket ? "原价 " + Money.format(p.marketPrice) : null);
            StockLabels.apply(stock, p.stockStatus);
            if (images != null) {
                images.load(image, p.imageUrl, imagePx);
            }
            itemView.setOnClickListener(v -> listener.onProductClick(p));
            boolean soldOut = "out_of_stock".equals(p.stockStatus);
            add.setText(soldOut ? "暂时缺货" : "加入购物车");
            add.setEnabled(!soldOut);
            add.setAlpha(soldOut ? 0.5f : 1f);
            add.setContentDescription(soldOut ? p.name + " 暂时缺货" : "把 " + p.name + " 加入购物车");
            add.setOnClickListener(v -> listener.onAddToCart(p));
        }
    }

    final class FooterHolder extends RecyclerView.ViewHolder {
        final TextView text;

        FooterHolder(View v) {
            super(v);
            text = v.findViewById(R.id.footer_text);
        }

        void bind(Footer f) {
            switch (f) {
                case LOADING:
                    text.setText("正在加载更多…");
                    break;
                case ERROR:
                    text.setText("加载失败，点击重试");
                    break;
                case END:
                    text.setText("已经到底了");
                    break;
                default:
                    text.setText("");
            }
            text.setClickable(f == Footer.ERROR);
            text.setOnClickListener(f == Footer.ERROR ? v -> listener.onRetryMore() : null);
        }
    }
}
