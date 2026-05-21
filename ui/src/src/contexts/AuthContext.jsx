import { createContext, useCallback, useContext, useEffect, useState } from 'react';
import { admin, catalog, ApiError } from '../api/client.js';

// Two scopes of session live in this app: the admin user (set by /api/v1/auth/*)
// and the catalog customer (set by /catalog/login). They're independent — an
// admin operator might also be a customer — so we expose both flags.

const AuthCtx = createContext(null);

export function AuthProvider({ children }) {
  const [adminUser, setAdminUser] = useState(undefined);   // undefined = unknown, null = unauthenticated
  const [catalogUser, setCatalogUser] = useState(undefined);

  const refreshAdmin = useCallback(async () => {
    try { setAdminUser(await admin.me()); }
    catch (e) {
      if (e instanceof ApiError && e.status === 401) setAdminUser(null);
      else setAdminUser(null);
    }
  }, []);

  const refreshCatalog = useCallback(async () => {
    try { setCatalogUser(await catalog.me()); }
    catch (e) {
      if (e instanceof ApiError && e.status === 401) setCatalogUser(null);
      else setCatalogUser(null);
    }
  }, []);

  const adminLogout = useCallback(async () => {
    try { await admin.logout(); } catch {}
    setAdminUser(null);
  }, []);

  const catalogLogout = useCallback(async () => {
    try { await catalog.logout(); } catch {}
    setCatalogUser(null);
  }, []);

  // Lazy: we don't fetch on mount globally; layouts call refresh when they mount.
  // That keeps the public login pages from making spurious 401 calls.

  return (
    <AuthCtx.Provider value={{
      adminUser, catalogUser,
      refreshAdmin, refreshCatalog,
      setAdminUser, setCatalogUser,
      adminLogout, catalogLogout,
    }}>
      {children}
    </AuthCtx.Provider>
  );
}

export function useAuth() {
  const ctx = useContext(AuthCtx);
  if (!ctx) throw new Error('useAuth must be used within <AuthProvider>');
  return ctx;
}
