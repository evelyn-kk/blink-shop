import { ProductDetailPage } from './pages/ProductDetailPage';
import { ProductListPage } from './pages/ProductListPage';
import { productsHref, useRoute } from './lib/router';
import { Empty } from './components/StateView';

export default function App() {
  const route = useRoute();
  return (
    <>
      <header className="topbar">
        <a className="brand" href={productsHref({})}>
          Blink Shop 管理端
        </a>
      </header>
      <main className="container">
        {route.name === 'products' && <ProductListPage query={route.query} />}
        {route.name === 'product' && <ProductDetailPage key={route.id} id={route.id} />}
        {route.name === 'not_found' && (
          <Empty text="页面不存在">
            <a className="button" href={productsHref({})}>
              返回商品列表
            </a>
          </Empty>
        )}
      </main>
    </>
  );
}
