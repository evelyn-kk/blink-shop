import { useState } from 'react';
import { logout } from './api/auth';
import { Empty } from './components/StateView';
import { RequireRole } from './components/RequireRole';
import { documentsHref, loginHref, merchantProductsHref, navigate, productsHref, useRoute } from './lib/router';
import { clearSession, useSession } from './lib/session';
import { DocumentDetailPage } from './pages/DocumentDetailPage';
import { DocumentFormPage } from './pages/DocumentFormPage';
import { DocumentsPage } from './pages/DocumentsPage';
import { LoginPage } from './pages/LoginPage';
import { MerchantProductFormPage } from './pages/MerchantProductFormPage';
import { MerchantProductsPage } from './pages/MerchantProductsPage';
import { ProductDetailPage } from './pages/ProductDetailPage';
import { ProductListPage } from './pages/ProductListPage';

export default function App() {
  const route = useRoute();
  return (
    <>
      <TopBar />
      <main className="container">
        {route.name === 'products' && <ProductListPage query={route.query} />}
        {route.name === 'product' && <ProductDetailPage key={route.id} id={route.id} />}
        {route.name === 'login' && <LoginPage next={route.next} />}
        {route.name === 'merchant_products' && (
          <RequireRole role="merchant">
            <MerchantProductsPage query={route.query} />
          </RequireRole>
        )}
        {route.name === 'merchant_product_new' && (
          <RequireRole role="merchant">
            <MerchantProductFormPage />
          </RequireRole>
        )}
        {route.name === 'merchant_product_edit' && (
          <RequireRole role="merchant">
            <MerchantProductFormPage key={route.id} id={route.id} />
          </RequireRole>
        )}
        {route.name === 'documents' && (
          <RequireRole role={route.scope}>
            <DocumentsPage key={route.scope} scope={route.scope} query={route.query} />
          </RequireRole>
        )}
        {route.name === 'document_new' && (
          <RequireRole role={route.scope}>
            <DocumentFormPage key={route.scope} scope={route.scope} />
          </RequireRole>
        )}
        {route.name === 'document' && (
          <RequireRole role={route.scope}>
            <DocumentDetailPage key={`${route.scope}|${route.id}`} scope={route.scope} id={route.id} />
          </RequireRole>
        )}
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

function TopBar() {
  const session = useSession();
  const [busy, setBusy] = useState(false);

  async function signOut() {
    setBusy(true);
    try {
      await logout();
    } catch {
      // token 已失效也照样清除本地会话
    } finally {
      clearSession();
      setBusy(false);
      navigate(productsHref({}));
    }
  }

  return (
    <header className="topbar">
      <a className="brand" href={productsHref({})}>
        Blink Shop 管理端
      </a>
      <nav className="topnav" aria-label="主导航">
        <a href={productsHref({})}>商品巡检</a>
        {session?.account.role === 'merchant' && (
          <>
            <a href={merchantProductsHref()}>我的商品</a>
            <a href={documentsHref('merchant')}>知识资料</a>
          </>
        )}
        {session?.account.role === 'admin' && <a href={documentsHref('admin')}>知识资料</a>}
      </nav>
      <div className="account">
        {session ? (
          <>
            <span className="muted small">{session.account.display_name}</span>
            <button type="button" className="button" onClick={signOut} disabled={busy}>
              退出
            </button>
          </>
        ) : (
          <a className="button" href={loginHref(window.location.hash)}>
            登录
          </a>
        )}
      </div>
    </header>
  );
}
