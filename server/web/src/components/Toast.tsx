import React, { createContext, useContext, useState, useCallback } from 'react';

export interface ToastMessage {
  id: string;
  type?: 'info' | 'success' | 'warning' | 'error';
  message: string;
}

interface ToastContextType {
  toast: (message: string, type?: 'info' | 'success' | 'warning' | 'error') => void;
}

const ToastContext = createContext<ToastContextType | null>(null);

export const ToastProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [toasts, setToasts] = useState<ToastMessage[]>([]);

  const toast = useCallback((message: string, type: 'info' | 'success' | 'warning' | 'error' = 'info') => {
    const id = Math.random().toString(36).substring(2, 9);
    setToasts((prev) => [...prev, { id, type, message }]);
    setTimeout(() => {
      setToasts((prev) => prev.filter((t) => t.id !== id));
    }, 4500);
  }, []);

  return (
    <ToastContext.Provider value={{ toast }}>
      {children}
      <div className="toast-container">
        {toasts.map((t) => (
          <div key={t.id} className={`toast-item toast-${t.type || 'info'}`}>
            <span>{t.message}</span>
            <button
              className="toast-close"
              onClick={() => setToasts((prev) => prev.filter((item) => item.id !== t.id))}
            >
              ×
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
};

export const useToast = () => {
  const ctx = useContext(ToastContext);
  if (!ctx) throw new Error('useToast must be used within ToastProvider');
  return ctx;
};
