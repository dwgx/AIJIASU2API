import { render } from '@testing-library/react'
import { MemoryRouter, Navigate, Route, Routes } from 'react-router-dom'
import { ToastProvider } from '../../src/components/Toast'
import { LanguageProvider } from '../../src/lib/i18n'
import { ThemeProvider } from '../../src/lib/theme'
import Orders from '../../src/pages/Orders'
import Tenants from '../../src/pages/Tenants'
import Accounts from '../../src/pages/Accounts'
import PortalActivate from '../../src/pages/portal/PortalActivate'
import PortalDashboard from '../../src/pages/portal/PortalDashboard'
import PortalLogin from '../../src/pages/portal/PortalLogin'
import PortalPending from '../../src/pages/portal/PortalPending'
import PortalSignup from '../../src/pages/portal/PortalSignup'

/**
 * Archived surfaces (customer portal, orders, tenant admin) are no longer in
 * the shipped router: AJS-P1-06 unmounted them and P2-09 deletes the source.
 * Their tests still pin the retained code, so they mount the pages directly
 * through this throwaway router. Delete this file together with the pages.
 */
export function renderArchivedApp() {
  const entry = `${window.location.pathname}${window.location.search}`
  return render(
    <LanguageProvider>
      <ThemeProvider>
        <ToastProvider>
          <MemoryRouter initialEntries={[entry]}>
            <Routes>
              <Route path="/portal/login" element={<PortalLogin />} />
              <Route path="/portal/signup" element={<PortalSignup />} />
              <Route path="/portal/pending" element={<PortalPending />} />
              <Route path="/portal/activate" element={<PortalActivate />} />
              <Route path="/portal" element={<PortalDashboard />} />
              <Route path="/portal/*" element={<Navigate to="/portal" replace />} />
              <Route path="/orders" element={<Orders />} />
              <Route path="/tenants" element={<Tenants />} />
              <Route path="/accounts" element={<Accounts />} />
              <Route path="*" element={<Navigate to="/portal" replace />} />
            </Routes>
          </MemoryRouter>
        </ToastProvider>
      </ThemeProvider>
    </LanguageProvider>,
  )
}
