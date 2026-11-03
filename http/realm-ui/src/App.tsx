import { useEffect, useState } from 'react';
import { Alert, Badge, Button, Label, Spinner, TextInput, ThemeProvider } from 'flowbite-react';
import { HiKey, HiLogout } from 'react-icons/hi';
import { QueryClient, QueryClientProvider, useMutation, useQuery } from '@tanstack/react-query';
import { BrowserRouter as Router, Routes, Route, Link, useLocation, useNavigate } from 'react-router-dom';
import { ThemeInit } from '../.flowbite-react/init';
import { trimPrefix, trimSuffix } from './utils/strings';
import { SideNav } from './components/side-nav';
import { ChamberView } from './components/ChamberView';
import { UNAUTHORIZED_EVENT, createChamber, getActor, getMe, listChambers, setActor, signOut } from './api';
import { ensureTrailingSlash } from './utils/path';
import { SessionContext, useSession } from './session';
import { SignIn } from './components/SignIn';
import { KeysDrawer } from './components/KeysDrawer';
import { RealmMark } from './components/RealmMark';
import { realmTheme } from './theme';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: 'always',
      refetchOnMount: 'always',
      refetchOnReconnect: 'always',
      retry: false,
      staleTime: Infinity,
    },
  },
});

export const App = () => {
  return (
    <ThemeProvider theme={realmTheme}>
      <ThemeInit />
      <QueryClientProvider client={queryClient}>
        <Router basename="/ui">
          <Routes>
            <Route
              path="*"
              element={
                <AuthGate>
                  <Content />
                </AuthGate>
              }
            />
          </Routes>
        </Router>
      </QueryClientProvider>
    </ThemeProvider>
  );
};

/** Shows the sign-in screen when the server requires it, otherwise provides the session. */
const AuthGate = ({ children }: { children: React.ReactNode }) => {
  const { data: me, error } = useQuery({ queryKey: ['me'], queryFn: getMe });

  // Any request refused for lack of a session re-checks who is signed in,
  // which shows the sign-in screen once the session has ended.
  useEffect(() => {
    const recheck = () => void queryClient.invalidateQueries({ queryKey: ['me'] });
    window.addEventListener(UNAUTHORIZED_EVENT, recheck);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, recheck);
  }, []);

  if (error instanceof Error) {
    return <Alert color="failure">Could not reach the realm server: {error.message}</Alert>;
  }
  if (!me) {
    return <Spinner aria-label="Loading" size="lg" className="m-8" />;
  }
  if (me.authEnabled && !me.user) {
    return <SignIn providers={me.providers} />;
  }
  return <SessionContext.Provider value={me}>{children}</SessionContext.Provider>;
};

/** The signed-in user, with sign-out and (for admins) API key management. */
const UserMenu = ({ path }: { path: string }) => {
  const me = useSession();
  const [keysOpen, setKeysOpen] = useState(false);
  if (!me.user) return null;
  const { isAdmin } = me;
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span
        className="text-sm text-gray-700 dark:text-gray-300"
        title={me.user.provider && `Signed in with ${me.user.provider}`}
      >
        {me.user.name || me.user.id}
      </span>
      <Badge color={isAdmin ? 'info' : 'gray'}>{me.user.role}</Badge>
      {isAdmin && (
        <>
          <Button size="xs" color="light" onClick={() => setKeysOpen(true)}>
            <HiKey className="mr-1" /> API keys
          </Button>
          <KeysDrawer key={path} path={path} open={keysOpen} onClose={() => setKeysOpen(false)} />
        </>
      )}
      <Button
        size="xs"
        color="light"
        onClick={() => {
          void signOut().then(() => queryClient.invalidateQueries({ queryKey: ['me'] }));
        }}
      >
        <HiLogout className="mr-1" /> Sign out
      </Button>
    </div>
  );
};

/** Lets an editor say who they are when the server has no sign-in; recorded with every change. */
const ActorField = () => {
  const [actor, setActorState] = useState(getActor);
  return (
    <div className="flex items-center gap-2">
      <Label htmlFor="actor" className="whitespace-nowrap">
        Editing as
      </Label>
      <TextInput
        id="actor"
        sizing="sm"
        placeholder="your name or email"
        value={actor}
        onChange={(e) => {
          setActorState(e.target.value);
          setActor(e.target.value);
        }}
      />
    </div>
  );
};

const Content = () => {
  const me = useSession();
  const { canEdit } = me;
  const [chamberName, setChamberName] = useState('');
  const location = useLocation();
  const navigate = useNavigate();
  const path = ensureTrailingSlash(location.pathname);

  const { data: children = [] } = useQuery({ queryKey: ['list', path], queryFn: () => listChambers(path) });

  const create = useMutation({
    mutationFn: (name: string) => createChamber(path + ensureTrailingSlash(trimPrefix(name.trim(), '/'))),
    onSuccess: (_, name) => {
      setChamberName('');
      void queryClient.invalidateQueries({ queryKey: ['list', path] });
      void navigate(path + trimSuffix(trimPrefix(name.trim(), '/'), '/'));
    },
  });

  const directories = children.filter((c) => c !== '.');
  const trimmed = trimSuffix(trimPrefix(location.pathname, '/'), '/');
  const up = trimmed !== '' ? trimmed.split('/') : [];

  return (
    <div className="min-h-screen">
      <header className="border-b border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-800">
        <div className="mx-auto flex w-full max-w-[1280px] flex-wrap items-center justify-between gap-3 px-4 py-3">
          <Link
            to="/"
            className="flex items-center gap-2 rounded-md text-lg font-semibold tracking-tight [font-stretch:92%] focus-visible:outline-2 focus-visible:outline-primary-500"
          >
            <RealmMark className="h-6 w-6 text-primary-700 dark:text-primary-400" />
            realm
          </Link>
          {me.authEnabled ? <UserMenu path={path} /> : <ActorField />}
        </div>
      </header>
      <div className="mx-auto flex w-full max-w-[1280px] flex-col gap-6 px-4 py-6">
        <ChamberPath segments={up} />
        <div className="grid grid-cols-1 gap-8 md:grid-cols-12">
          <aside className="flex flex-col gap-6 md:col-span-3">
            {directories.length > 0 && <SideNav directories={directories} />}
            {canEdit && (
              <form
                className="flex flex-col gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  create.mutate(chamberName);
                }}
              >
                <Label htmlFor="chamber" className="text-gray-600 dark:text-gray-400">
                  New child chamber
                </Label>
                <div className="flex gap-2">
                  <TextInput
                    id="chamber"
                    sizing="sm"
                    required
                    className="min-w-0 flex-1 [&_input]:font-mono"
                    placeholder="prod"
                    value={chamberName}
                    onChange={(e) => setChamberName(e.target.value)}
                  />
                  <Button type="submit" size="sm" color="light" disabled={create.isPending}>
                    Create
                  </Button>
                </div>
                {create.error instanceof Error && <Alert color="failure">{create.error.message}</Alert>}
              </form>
            )}
          </aside>
          <main className="md:col-span-9">
            <ChamberView key={path} path={path} />
          </main>
        </div>
      </div>
    </div>
  );
};

/**
 * The page heading: the chamber's path, with each ancestor linked so the
 * inheritance chain reads left to right.
 */
const ChamberPath = ({ segments }: { segments: string[] }) => {
  const slash = <span className="px-0.5 text-gray-400 dark:text-gray-500">/</span>;
  return (
    <nav aria-label="Chamber path">
      <h1 className="flex flex-wrap items-baseline font-mono text-2xl font-medium tracking-tight sm:text-3xl">
        {segments.length === 0 ? (
          <span>/</span>
        ) : (
          <Link to="/" aria-label="All chambers" className="text-gray-400 hover:text-primary-700 dark:text-gray-500">
            /
          </Link>
        )}
        {segments.map((segment, index) => {
          const last = index === segments.length - 1;
          return (
            <span key={index} className="flex items-baseline">
              {index > 0 && slash}
              {last ? (
                <span aria-current="page">{segment}</span>
              ) : (
                <Link
                  to={'/' + segments.slice(0, index + 1).join('/')}
                  className="text-gray-500 hover:text-primary-700 dark:text-gray-400 dark:hover:text-primary-300"
                >
                  {segment}
                </Link>
              )}
            </span>
          );
        })}
      </h1>
    </nav>
  );
};
