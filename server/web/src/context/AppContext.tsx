import React, { createContext, useContext, useEffect, useState, useCallback, useRef } from 'react';
import { Printer, Job, Agent, Candidate } from '../types';
import { api, getStoredToken, setStoredToken, TOKEN_KEY, THEME_KEY } from '../api';

interface AppContextType {
  token: string;
  setToken: (t: string) => void;
  isAuthenticated: boolean;
  logout: () => void;

  theme: 'dark' | 'light';
  toggleTheme: () => void;

  autoRefresh: boolean;
  setAutoRefresh: (val: boolean) => void;
  sseConnected: boolean;

  printers: Printer[];
  jobs: Job[];
  agents: Agent[];
  candidates: Candidate[];
  fleetStats: {
    total: number;
    usable: number;
    notUsable: number;
    queuedTotal: number;
  };

  loading: boolean;
  lastUpdated: Date | null;
  refreshAll: () => Promise<void>;
  refreshPrinters: () => Promise<void>;
  refreshJobs: () => Promise<void>;
  refreshAgents: () => Promise<void>;
  refreshCandidates: () => Promise<void>;
}

const AppContext = createContext<AppContextType | null>(null);

export const AppProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [token, setTokenState] = useState<string>(getStoredToken());
  const [theme, setTheme] = useState<'dark' | 'light'>('dark');
  const [autoRefresh, setAutoRefresh] = useState<boolean>(true);
  const [sseConnected, setSseConnected] = useState<boolean>(false);

  const [printers, setPrinters] = useState<Printer[]>([]);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [loading, setLoading] = useState<boolean>(false);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);

  const sseAbortRef = useRef<AbortController | null>(null);

  // Init theme
  useEffect(() => {
    const savedTheme = (localStorage.getItem(THEME_KEY) as 'dark' | 'light') || 'dark';
    setTheme(savedTheme);
    document.documentElement.dataset.theme = savedTheme;
  }, []);

  const toggleTheme = () => {
    const next = theme === 'dark' ? 'light' : 'dark';
    setTheme(next);
    localStorage.setItem(THEME_KEY, next);
    document.documentElement.dataset.theme = next;
  };

  const setToken = (t: string) => {
    setTokenState(t);
    setStoredToken(t);
  };

  const logout = () => {
    setToken('');
    setPrinters([]);
    setJobs([]);
    setAgents([]);
    setCandidates([]);
    if (sseAbortRef.current) {
      sseAbortRef.current.abort();
      sseAbortRef.current = null;
    }
    setSseConnected(false);
  };

  const refreshPrinters = useCallback(async () => {
    if (!token) return;
    try {
      const res = await api.getFleet();
      // The server lists printers in map order, which changes on every call: sort so tiles
      // and rows keep their place across refreshes.
      setPrinters(
        (res.items || []).sort(
          (a, b) =>
            (a.name || a.printer_id).localeCompare(b.name || b.printer_id) ||
            a.printer_id.localeCompare(b.printer_id),
        ),
      );
      setLastUpdated(new Date());
    } catch (e: any) {
      if (e.status === 401) logout();
    }
  }, [token]);

  const refreshJobs = useCallback(async () => {
    if (!token) return;
    try {
      const res = await api.getJobs(50);
      setJobs(res.items || []);
    } catch {
      // ignore non-fatal
    }
  }, [token]);

  const refreshAgents = useCallback(async () => {
    if (!token) return;
    try {
      const res = await api.getAgents();
      setAgents(res.items || []);
    } catch {
      // ignore
    }
  }, [token]);

  const refreshCandidates = useCallback(async () => {
    if (!token) return;
    try {
      const res = await api.getCandidates(false);
      setCandidates(res.items || []);
    } catch {
      // ignore
    }
  }, [token]);

  const refreshAll = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    await Promise.allSettled([
      refreshPrinters(),
      refreshJobs(),
      refreshAgents(),
      refreshCandidates(),
    ]);
    setLoading(false);
  }, [token, refreshPrinters, refreshJobs, refreshAgents, refreshCandidates]);

  // Initial load on token
  useEffect(() => {
    if (token) {
      refreshAll();
    }
  }, [token]);

  // Controlled polling: runs every 5 seconds only if autoRefresh is true
  useEffect(() => {
    if (!token || !autoRefresh) return;
    const interval = setInterval(() => {
      refreshPrinters();
      refreshJobs();
    }, 5000);
    return () => clearInterval(interval);
  }, [token, autoRefresh, refreshPrinters, refreshJobs]);

  // SSE Stream
  useEffect(() => {
    if (!token) {
      setSseConnected(false);
      return;
    }

    let active = true;
    const connectSSE = async () => {
      if (sseAbortRef.current) {
        sseAbortRef.current.abort();
      }
      const controller = new AbortController();
      sseAbortRef.current = controller;

      try {
        const res = await fetch('/api/v1/events?topics=printer.status,job.state', {
          headers: {
            Authorization: `Bearer ${token}`,
            Accept: 'text/event-stream',
          },
          signal: controller.signal,
        });

        if (!res.ok || !res.body) {
          setSseConnected(false);
          return;
        }

        if (active) setSseConnected(true);

        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buffer = '';

        while (active) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });

          let splitIdx: number;
          while ((splitIdx = buffer.indexOf('\n\n')) >= 0) {
            const frame = buffer.slice(0, splitIdx);
            buffer = buffer.slice(splitIdx + 2);

            const evMatch = /^event:\s*(.+)$/m.exec(frame);
            const evType = evMatch ? evMatch[1].trim() : '';

            if (evType === 'printer.status') {
              refreshPrinters();
            } else if (evType === 'job.state') {
              refreshJobs();
              refreshPrinters();
            }
          }
        }
      } catch (err: any) {
        if (err.name !== 'AbortError' && active) {
          setSseConnected(false);
          // Reconnect with backoff
          setTimeout(() => {
            if (active) connectSSE();
          }, 5000);
        }
      }
    };

    connectSSE();

    return () => {
      active = false;
      if (sseAbortRef.current) {
        sseAbortRef.current.abort();
        sseAbortRef.current = null;
      }
      setSseConnected(false);
    };
  }, [token, refreshPrinters, refreshJobs]);

  const queuedTotal = printers.reduce((acc, p) => acc + (p.queue_depth || 0), 0);
  const usable = printers.filter((p) => p.usable).length;
  const notUsable = printers.length - usable;

  return (
    <AppContext.Provider
      value={{
        token,
        setToken,
        isAuthenticated: !!token,
        logout,
        theme,
        toggleTheme,
        autoRefresh,
        setAutoRefresh,
        sseConnected,
        printers,
        jobs,
        agents,
        candidates,
        fleetStats: {
          total: printers.length,
          usable,
          notUsable,
          queuedTotal,
        },
        loading,
        lastUpdated,
        refreshAll,
        refreshPrinters,
        refreshJobs,
        refreshAgents,
        refreshCandidates,
      }}
    >
      {children}
    </AppContext.Provider>
  );
};

export const useApp = () => {
  const ctx = useContext(AppContext);
  if (!ctx) throw new Error('useApp must be used within AppProvider');
  return ctx;
};
