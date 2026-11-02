import { createContext, useContext } from 'react';
import { Me } from './models/response';

export type Session = {
  me: Me;
  /** Whether the user may change chambers. */
  canEdit: boolean;
  /** Whether the user may manage API keys and the catalog. */
  isAdmin: boolean;
};

const open: Session = {
  me: { authEnabled: false, user: null, providers: [] },
  canEdit: true,
  isAdmin: true,
};

export const sessionFor = (me: Me): Session => {
  if (!me.authEnabled) {
    return { ...open, me };
  }
  const role = me.user?.role ?? 'none';
  return { me, canEdit: role === 'editor' || role === 'admin', isAdmin: role === 'admin' };
};

export const SessionContext = createContext<Session>(open);

export const useSession = () => useContext(SessionContext);
