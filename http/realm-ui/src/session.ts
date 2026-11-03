import { createContext, useContext } from 'react';
import { Me } from './models/response';

/** Who is signed in and what they may do, as reported by the server. */
export const SessionContext = createContext<Me>({
  authEnabled: false,
  user: null,
  canEdit: true,
  isAdmin: true,
  providers: [],
});

export const useSession = () => useContext(SessionContext);
