import { Suspense, lazy } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AppShell } from './components/AppShell'
import { Skeleton } from './components/primitives'
import { ToastProvider } from './components/Toast'
import { LanguageProvider, useI18n } from './lib/i18n'
import { ThemeProvider } from './lib/theme'

const Accounts = lazy(() => import('./pages/Accounts'))
const Backup = lazy(() => import('./pages/Backup'))
const DevicePool = lazy(() => import('./pages/DevicePool'))
const Lab = lazy(() => import('./pages/Lab'))
const Leases = lazy(() => import('./pages/Leases'))
const Logs = lazy(() => import('./pages/Logs'))
const Nodes = lazy(() => import('./pages/Nodes'))
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

export default function App() {
  return (
    <LanguageProvider>
      <ThemeProvider>
        <ToastProvider>
          <BrowserRouter>
            <AppShell>
              <Suspense fallback={<RouteFallback />}>
                <Routes>
                  <Route path="/" element={<Overview />} />
                  <Route path="/usage" element={<Usage />} />
                  <Route path="/accounts" element={<Accounts />} />
                  <Route path="/nodes" element={<Nodes />} />
                  <Route path="/leases" element={<Leases />} />
                  <Route path="/device-pool" element={<DevicePool />} />
                  <Route path="/users" element={<Users />} />
                  <Route path="/requests" element={<Requests />} />
                  <Route path="/logs" element={<Logs />} />
                  <Route path="/schedules" element={<Schedules />} />
                  <Route path="/lab" element={<Lab />} />
                  <Route path="/settings" element={<Settings />} />
                  <Route path="/backup" element={<Backup />} />
                  <Route path="*" element={<Navigate to="/" replace />} />
                </Routes>
              </Suspense>
            </AppShell>
          </BrowserRouter>
        </ToastProvider>
      </ThemeProvider>
    </LanguageProvider>
  )
}
