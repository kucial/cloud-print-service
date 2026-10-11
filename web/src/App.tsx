import React, { useState, useEffect } from 'react';
import { useApp } from './context/AppContext';
import { Header, NavTab } from './components/Header';
import { Login } from './components/Login';
import { OverviewView } from './views/OverviewView';
import { PrintersView } from './views/PrintersView';
import { AgentsView } from './views/AgentsView';
import { DiscoveryView } from './views/DiscoveryView';
import { JobsView } from './views/JobsView';
import { TokensView } from './views/TokensView';
import { PreviewView } from './views/PreviewView';
import { OpsView } from './views/OpsView';

export const AppContent: React.FC = () => {
  const { isAuthenticated } = useApp();

  const getInitialTab = (): NavTab => {
    const hash = window.location.hash.replace('#', '') as NavTab;
    const validTabs: NavTab[] = [
      'overview',
      'printers',
      'agents',
      'discovery',
      'jobs',
      'tokens',
      'preview',
      'ops',
    ];
    return validTabs.includes(hash) ? hash : 'overview';
  };

  const [currentTab, setCurrentTab] = useState<NavTab>(getInitialTab);
  const [selectedPrinterId, setSelectedPrinterId] = useState<string | null>(null);

  // Sync hash with tab
  useEffect(() => {
    const onHashChange = () => {
      const hash = window.location.hash.replace('#', '') as NavTab;
      const validTabs: NavTab[] = [
        'overview',
        'printers',
        'agents',
        'discovery',
        'jobs',
        'tokens',
        'preview',
        'ops',
      ];
      if (validTabs.includes(hash)) {
        setCurrentTab(hash);
      }
    };

    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  const handleTabChange = (tab: NavTab) => {
    setCurrentTab(tab);
    window.location.hash = tab;
  };

  const handleSelectPrinter = (id: string | null) => {
    setSelectedPrinterId(id);
    if (id) {
      handleTabChange('printers');
    }
  };

  if (!isAuthenticated) {
    return <Login />;
  }

  return (
    <div className="app-layout">
      <Header currentTab={currentTab} setTab={handleTabChange} />

      <main className="main-content">
        {currentTab === 'overview' && (
          <OverviewView setTab={handleTabChange} onSelectPrinter={handleSelectPrinter} />
        )}
        {currentTab === 'printers' && (
          <PrintersView
            selectedPrinterId={selectedPrinterId}
            onSelectPrinter={setSelectedPrinterId}
          />
        )}
        {currentTab === 'agents' && <AgentsView />}
        {currentTab === 'discovery' && <DiscoveryView />}
        {currentTab === 'jobs' && <JobsView />}
        {currentTab === 'tokens' && <TokensView />}
        {currentTab === 'preview' && <PreviewView />}
        {currentTab === 'ops' && <OpsView />}
      </main>
    </div>
  );
};
