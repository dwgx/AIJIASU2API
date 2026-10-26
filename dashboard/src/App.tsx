import { Suspense, lazy } from 'react'
import { BrowserRouter, Navigate, Outlet, Route, Routes } from 'react-router-dom'
import { AppShell } from './components/AppShell'
import { Skeleton } from './components/primitives'
import { ToastProvider } from './components/Toast'
import { LanguageProvider, useI18n } from './lib/i18n'
import { ThemeProvider } from './lib/theme'

const Accounts = lazy(() => import('./pages/Accounts'))
const Backup = lazy(() => import('./pages/Backup'))
const Debug = lazy(() => import('./pages/Debug'))
const DevicePool = lazy(() => import('./pages/DevicePool'))
const Lab = lazy(() => import('./pages/Lab'))
const Leases = lazy(() => import('./pages/Leases'))
const Logs = lazy(() => import('./pages/Logs'))
const Nodes = lazy(() => import('./pages/Nodes'))
const Operations = lazy(() => import('./pages/Operations'))
const Overview = lazy(() => import('./pages/Overview'))
const Requests = lazy(() => import('./pages/Requests'))
const Schedules = lazy(() => import('./pages/Schedules'))
const Settings = lazy(() => import('./pages/Settings'))
const Usage = lazy(() => import('./pages/Usage'))
const Users = lazy(() => import('./pages/Users'))

function RouteFallback() {
  const { t } = useI18n()
  return (
    <div className="stack-lg" aria-busy="true" aria-label={t('shell.loadingPage')}>
      <Skeleton width={180} height={24} />
      <Skeleton height={320} />
    </div>
  )
}

/** Operator console: admin chrome, admin token gate, admin navigation. */
function AdminLayout() {
  return (
    <AppShell>
      <Suspense fallback={<RouteFallback />}>
        <Outlet />
      </Suspense>
    </AppShell>
  )
}

export default function App() {
  return (
    <LanguageProvider>
      <ThemeProvider>
        <ToastProvider>
          <BrowserRouter>
            <Routes>
              <Route element={<AdminLayout />}>
                <Route path="/" element={<Overview />} />
                <Route path="/usage" element={<Usage />} />
                <Route path="/accounts" element={<Accounts />} />
                <Route path="/nodes" element={<Nodes />} />
                <Route path="/operations" element={<Operations />} />
                <Route path="/leases" element={<Leases />} />
                <Route path="/device-pool" element={<DevicePool />} />
                <Route path="/users" element={<Users />} />
                <Route path="/requests" element={<Requests />} />
                <Route path="/logs" element={<Logs />} />
                <Route path="/debug" element={<Debug />} />
                <Route path="/schedules" element={<Schedules />} />
                <Route path="/lab" element={<Lab />} />
                <Route path="/settings" element={<Settings />} />
                <Route path="/backup" element={<Backup />} />
                <Route path="*" element={<Navigate to="/" replace />} />
              </Route>
            </Routes>
          </BrowserRouter>
        </ToastProvider>
      </ThemeProvider>
    </LanguageProvider>
  )
}
