import { Suspense, lazy } from 'react'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom'
import { AppShell } from './components/AppShell'
import { Skeleton } from './components/primitives'
import { ToastProvider } from './components/Toast'
import { ThemeProvider } from './lib/theme'

const Accounts = lazy(() => import('./pages/Accounts'))
const Lab = lazy(() => import('./pages/Lab'))
const Leases = lazy(() => import('./pages/Leases'))
const Logs = lazy(() => import('./pages/Logs'))
const Nodes = lazy(() => import('./pages/Nodes'))
const Overview = lazy(() => import('./pages/Overview'))
const Requests = lazy(() => import('./pages/Requests'))
const Settings = lazy(() => import('./pages/Settings'))
const Users = lazy(() => import('./pages/Users'))

function RouteFallback() {
  return (
    <div className="stack-lg" aria-busy="true" aria-label="Loading page">
      <Skeleton width={180} height={24} />
      <Skeleton height={320} />
    </div>
  )
}

export default function App() {
  return (
    <ThemeProvider>
      <ToastProvider>
        <BrowserRouter>
          <AppShell>
            <Suspense fallback={<RouteFallback />}>
              <Routes>
                <Route path="/" element={<Overview />} />
                <Route path="/accounts" element={<Accounts />} />
                <Route path="/nodes" element={<Nodes />} />
                <Route path="/leases" element={<Leases />} />
                <Route path="/users" element={<Users />} />
                <Route path="/requests" element={<Requests />} />
                <Route path="/logs" element={<Logs />} />
                <Route path="/lab" element={<Lab />} />
                <Route path="/settings" element={<Settings />} />
                <Route path="*" element={<Navigate to="/" replace />} />
              </Routes>
            </Suspense>
          </AppShell>
        </BrowserRouter>
      </ToastProvider>
    </ThemeProvider>
  )
}
