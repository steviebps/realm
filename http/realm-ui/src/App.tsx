import { useState } from 'react';
import { Alert, Breadcrumb, BreadcrumbItem, Button, Label, TextInput } from 'flowbite-react';
import { HiHome } from 'react-icons/hi';
import { QueryClient, QueryClientProvider, useMutation, useQuery } from 'react-query';
import { BrowserRouter as Router, Routes, Route, Link, useLocation, useNavigate } from 'react-router-dom';
import { ThemeInit } from '../.flowbite-react/init';
import { trimPrefix, trimSuffix } from './utils/strings';
import { SideNav } from './components/side-nav';
import { ChamberView } from './components/ChamberView';
import { createChamber, getActor, listChambers, setActor } from './api';
import { ensureTrailingSlash } from './utils/path';

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
            <Route path="*" element={<Content />} />
          </Routes>
        </Router>
      </QueryClientProvider>
    </>
  );
};

/** Lets an editor say who they are; recorded with every change until sign-in exists. */
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
        <ActorField />
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
        </aside>
        <main className="md:col-span-9">
          <ChamberView key={path} path={path} />
        </main>
      </div>
    </div>
  );
};
