import { Navigate, Route, Routes } from 'react-router-dom';
import { CatalogAuthProvider } from './contexts/CatalogAuthContext.jsx';

import Login from './pages/Login.jsx';

import AdminLogin from './pages/admin/Login.jsx';
import AdminLayout from './pages/admin/Layout.jsx';
import Packages from './pages/admin/Packages.jsx';
import UpstreamCredentials from './pages/admin/UpstreamCredentials.jsx';
import Licenses from './pages/admin/Licenses.jsx';
import Customers from './pages/admin/Customers.jsx';
import OIDC from './pages/admin/OIDC.jsx';
import Audit from './pages/admin/Audit.jsx';
import Config from './pages/admin/Config.jsx';
import Monitoring from './pages/admin/Monitoring.jsx';

import CatalogLogin from './pages/catalog/CatalogLogin.jsx';
import CatalogLayout from './pages/catalog/CatalogLayout.jsx';
import CatalogHome from './pages/catalog/CatalogHome.jsx';
import CatalogPackage from './pages/catalog/CatalogPackage.jsx';
import CatalogCredentials from './pages/catalog/CatalogCredentials.jsx';

export default function AppRoutes() {
  return (
    <Routes>
      <Route path="/" element={<Navigate to="/login" replace />} />
      <Route path="/login" element={<Login />} />

      <Route path="/admin/login" element={<AdminLogin />} />
      <Route path="/admin" element={<AdminLayout />}>
        <Route index element={<Navigate to="packages" replace />} />
        <Route path="packages" element={<Packages />} />
        <Route path="customers" element={<Customers />} />
        <Route path="licenses" element={<Licenses />} />
        {/* /admin/root-keys is preserved as a deep-link redirect into the
            Licenses → Root keys tab so any existing bookmarks keep working. */}
        <Route path="root-keys" element={<Navigate to="/admin/licenses?tab=root-keys" replace />} />
        <Route path="upstream-credentials" element={<UpstreamCredentials />} />
        <Route path="oidc" element={<OIDC />} />
        <Route path="audit" element={<Audit />} />
        <Route path="monitoring" element={<Monitoring />} />
        <Route path="config" element={<Config />} />
      </Route>

      <Route
        path="/catalog/*"
        element={
          <CatalogAuthProvider>
            <CatalogRoutes />
          </CatalogAuthProvider>
        }
      />

      <Route path="*" element={<NotFound />} />
    </Routes>
  );
}

function CatalogRoutes() {
  return (
    <Routes>
      <Route path="login" element={<CatalogLogin />} />
      <Route element={<CatalogLayout />}>
        <Route index element={<CatalogHome />} />
        <Route path="p/:slug" element={<CatalogPackage />} />
        <Route path="credentials" element={<CatalogCredentials />} />
      </Route>
      <Route path="*" element={<Navigate to="" replace />} />
    </Routes>
  );
}

function NotFound() {
  return (
    <div className="min-h-screen flex items-center justify-center bg-g-canvas">
      <div className="text-center">
        <div className="text-5xl font-bold text-g-text-disabled">404</div>
        <div className="mt-2 text-g-text-secondary">Page not found.</div>
      </div>
    </div>
  );
}
