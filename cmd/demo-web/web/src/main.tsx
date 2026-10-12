import React from 'react';
import ReactDOM from 'react-dom/client';
import { I18nProvider } from './i18n/context';
import { App } from './App';
import './app.css';

const rootEl = document.getElementById('root');
if (rootEl) {
  ReactDOM.createRoot(rootEl).render(
    <React.StrictMode>
      <I18nProvider>
        <App />
      </I18nProvider>
    </React.StrictMode>
  );
}
