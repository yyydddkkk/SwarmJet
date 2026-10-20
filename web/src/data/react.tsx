import { createContext, useContext, useEffect, useMemo, useSyncExternalStore, type ReactNode } from 'react';
import { browserDataSource, createProjectStore, type DataSource, type ProjectStore } from './store';
const SourceContext = createContext<DataSource>(browserDataSource);
export function DataProvider({source = browserDataSource, children}: {source?: DataSource; children: ReactNode}) { return <SourceContext.Provider value={source}>{children}</SourceContext.Provider>; }
export const useDataSource = () => useContext(SourceContext);
export const useDataAPI = () => useDataSource().api;
export function useProjectStore(id: string) {
  const source = useDataSource();
  const store = useMemo(() => createProjectStore(id, source), [id, source]);
  useEffect(() => { store.start(); return store.stop; }, [store]);
  return store;
}
export const useProjectState = (store: ProjectStore) => useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);
