import { useEffect, useState } from 'react';
import { useNavigate, useLocation } from 'react-router-dom';
import { admin } from '../../api/client.js';
import { useAuth } from '../../contexts/AuthContext.jsx';
import Card from '../../components/Card.jsx';
import Button from '../../components/Button.jsx';
import Input from '../../components/Input.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';
import TopBar from '../../components/TopBar.jsx';
import ThemeToggle from '../../components/ThemeToggle.jsx';
import { brand } from '../../brand/index.js';

export default function AdminLogin() {
  const nav = useNavigate();
  const loc = useLocation();
  const { setAdminUser } = useAuth();
  const next = new URLSearchParams(loc.search).get('next') || '/admin';

  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState(null);

  // Admins sign in with email/password (or a STATIC_ADMINS entry). OIDC was
  // intentionally removed from the admin surface — IdP-based sign-in is for
  // customers via /catalog/login.

  const onSubmit = async (e) => {
    e.preventDefault();
    setErr(null);
    setSubmitting(true);
    try {
      const result = await admin.login(email, password);
      if (result && result.email) setAdminUser(result);
      else { try { setAdminUser(await admin.me()); } catch {} }
      nav(next, { replace: true });
    } catch (e2) {
      setErr(e2);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="min-h-screen flex flex-col bg-g-canvas">
      <TopBar area="admin" linkBrand={false} rightSlot={<ThemeToggle />} />
      <div className="flex-1 flex items-center justify-center p-6">
        <div className="w-full max-w-sm">
          <div className="text-center mb-6">
            <h1 className="text-2xl font-semibold tracking-tight text-g-text">Sign in to the admin console</h1>
            <p className="mt-2 text-sm text-g-text-secondary">Use your administrator credentials.</p>
          </div>
          <Card elevated padding="lg" className="border-g-border-weak">
          <form onSubmit={onSubmit} className="space-y-3">
            <Input
              id="email"
              type="email"
              label="Email"
              autoComplete="username"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
            />
            <Input
              id="password"
              type="password"
              label="Password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
            <ErrorBanner error={err} />
            <Button
              type="submit"
              variant="primary"
              size="lg"
              loading={submitting}
              className="w-full"
            >
              {submitting ? 'Signing in…' : 'Sign in'}
            </Button>
          </form>

          </Card>
          <div className="mt-4 text-center text-xs text-g-text-secondary">
            Customer? <a href="/catalog/login" className="text-g-text-link">Open the catalog</a>
          </div>
          <div className="mt-2 text-center text-xs text-g-text-disabled">
            <a href="/login" className="hover:text-g-text-secondary transition-colors">Use single sign-on</a>
          </div>
        </div>
      </div>
      <footer className="text-center py-4 text-xs text-g-text-disabled">
        {brand.footerTagline}
      </footer>
    </div>
  );
}
