import React from 'react';
import ReactDOM from 'react-dom/client';
import { I18nProvider } from './i18n/context';
import { AppProvider } from './context/AppContext';
import { ToastProvider } from './components/Toast';
import { AppContent } from './App';
import './app.css';

const rootEl = document.getElementById('app');
if (rootEl) {
  ReactDOM.createRoot(rootEl).render(
    <React.StrictMode>
      <I18nProvider>
        <AppProvider>
          <ToastProvider>
            <AppContent />
          </ToastProvider>
        </AppProvider>
      </I18nProvider>
    </React.StrictMode>
  );
}
