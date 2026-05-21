import { createContext, useCallback, useContext, useEffect, useState } from 'react';
import { catalog, ApiError } from '../api/client.js';

// Customer-side session, distinct from admin. A customer using docker-login
// credentials (token_id:secret) authenticates via /catalog/login, which sets
// the ag_customer_session cookie. License expiry → catalog.me() returns 401
// and the gate sends them back to /catalog/login.

const CatalogAuthCtx = createContext(null);

export function CatalogAuthProvider({ children }) {
  const [session, setSession] = useState(undefined); // undefined = unknown
  const [error, setError] = useState(null);

  const refresh = useCallback(async () => {
    try {
      const me = await catalog.me();
      setSession(me);
      setError(null);
      return me;
    } catch (err) {
      if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
        setSession(null);
        setError(null);
      } else {
        setSession(null);
        setError(err);
      }
      return null;
    }
  }, []);

  useEffect(() => { refresh(); }, [refresh]);

  const login = useCallback(async (tokenId, secret) => {
    await catalog.login(tokenId, secret);
    return refresh();
  }, [refresh]);

  const logout = useCallback(async () => {
    try { await catalog.logout(); } catch { /* ignore */ }
    setSession(null);
  }, []);

  return (
    <CatalogAuthCtx.Provider value={{ session, error, refresh, login, logout }}>
      {children}
    </CatalogAuthCtx.Provider>
  );
}

export function useCatalogAuth() {
  const ctx = useContext(CatalogAuthCtx);
  if (!ctx) throw new Error('useCatalogAuth must be used within <CatalogAuthProvider>');
  return ctx;
}
