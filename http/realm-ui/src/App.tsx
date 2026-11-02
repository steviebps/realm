import { useEffect, useState } from 'react';
import { Alert, Badge, Breadcrumb, BreadcrumbItem, Button, Label, Spinner, TextInput } from 'flowbite-react';
import { HiHome, HiKey, HiLogout } from 'react-icons/hi';
import { QueryClient, QueryClientProvider, useMutation, useQuery } from 'react-query';
import { BrowserRouter as Router, Routes, Route, Link, useLocation, useNavigate } from 'react-router-dom';
import { ThemeInit } from '../.flowbite-react/init';
import { trimPrefix, trimSuffix } from './utils/strings';
import { SideNav } from './components/side-nav';
import { ChamberView } from './components/ChamberView';
import { UNAUTHORIZED_EVENT, createChamber, getActor, getMe, listChambers, setActor, signOut } from './api';
import { ensureTrailingSlash } from './utils/path';
import { SessionContext, sessionFor, useSession } from './session';
import { SignIn } from './components/SignIn';
import { KeysDrawer } from './components/KeysDrawer';

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
    <>
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
    </>
  );
};

/** Shows the sign-in screen when the server requires it, otherwise provides the session. */
const AuthGate = ({ children }: { children: React.ReactNode }) => {
  const { data: me, error } = useQuery('me', getMe);

  // Any request refused for lack of a session re-checks who is signed in,
  // which shows the sign-in screen once the session has ended.
  useEffect(() => {
    const recheck = () => void queryClient.invalidateQueries('me');
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
  return <SessionContext.Provider value={sessionFor(me)}>{children}</SessionContext.Provider>;
};

/** The signed-in user, with sign-out and (for admins) API key management. */
const UserMenu = ({ path }: { path: string }) => {
  const { me, isAdmin } = useSession();
  const [keysOpen, setKeysOpen] = useState(false);
  if (!me.user) return null;
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="text-sm" title={me.user.provider && `Signed in with ${me.user.provider}`}>
        {me.user.name || me.user.id}
      </span>
      <Badge color={isAdmin ? 'purple' : 'gray'}>{me.user.role}</Badge>
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
          void signOut().then(() => queryClient.invalidateQueries('me'));
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
  const { me, canEdit } = useSession();
  const [chamberName, setChamberName] = useState('');
  const location = useLocation();
  const navigate = useNavigate();
  const path = ensureTrailingSlash(location.pathname);

  const { data: children = [] } = useQuery(['list', path], () => listChambers(path));

  const create = useMutation(
    (name: string) => createChamber(path + ensureTrailingSlash(trimPrefix(name.trim(), '/'))),
    {
      onSuccess: (_, name) => {
        setChamberName('');
        void queryClient.invalidateQueries(['list', path]);
        void navigate(path + trimSuffix(trimPrefix(name.trim(), '/'), '/'));
      },
    }
  );

  const directories = children.filter((c) => c !== '.');
  const trimmed = trimSuffix(trimPrefix(location.pathname, '/'), '/');
  const up = trimmed !== '' ? trimmed.split('/') : [];

  return (
    <div className="mx-auto my-5 flex w-full max-w-[1280px] flex-col gap-4 px-4">
      <header className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-2xl font-bold">Realm</h1>
        {me.authEnabled ? <UserMenu path={path} /> : <ActorField />}
      </header>
      <Breadcrumb aria-label="directory crumbs" className="py-1">
        <BreadcrumbItem icon={HiHome}>
          <Link to="/" relative="path">
            Home
          </Link>
        </BreadcrumbItem>
        {up.map((segment, index) => (
          <BreadcrumbItem key={index}>
            <Link to={'/' + [...up.slice(0, index), segment].join('/')}>{segment}</Link>
          </BreadcrumbItem>
        ))}
      </Breadcrumb>
      <div className="grid grid-cols-1 gap-5 md:grid-cols-12">
        <aside className="flex flex-col gap-4 md:col-span-3">
          {directories.length > 0 && <SideNav directories={directories} />}
          {canEdit && (
            <form
              className="flex flex-col gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                create.mutate(chamberName);
              }}
            >
              <Label htmlFor="chamber">New child chamber</Label>
              <TextInput
                id="chamber"
                required
                placeholder="prod"
                value={chamberName}
                onChange={(e) => setChamberName(e.target.value)}
              />
              <Button type="submit" size="sm" disabled={create.isLoading}>
                Create chamber
              </Button>
              {create.error instanceof Error && <Alert color="failure">{create.error.message}</Alert>}
            </form>
          )}
        </aside>
        <main className="md:col-span-9">
          <ChamberView key={path} path={path} />
        </main>
      </div>
    </div>
  );
};
