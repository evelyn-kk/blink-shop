import { useState } from 'react';

// 商品图：地址为空或加载失败时显示占位块，保持布局不塌陷。
export function ProductImage({ src, alt, className = '' }: { src: string; alt: string; className?: string }) {
  const [failed, setFailed] = useState(false);
  if (!src || failed) {
    return (
      <div className={`product-image placeholder ${className}`} role="img" aria-label={`${alt}（暂无图片）`}>
        暂无图片
      </div>
    );
  }
  return (
    <img className={`product-image ${className}`} src={src} alt={alt} loading="lazy" onError={() => setFailed(true)} />
  );
}
