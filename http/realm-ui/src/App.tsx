import { useState } from 'react';
import { Breadcrumb, BreadcrumbItem, Button, Label, Spinner, TextInput } from 'flowbite-react';
import { HiHome } from 'react-icons/hi';
import { QueryClient, QueryClientProvider, useMutation, useQuery } from 'react-query';
import { BrowserRouter as Router, Routes, Route, Link, useLocation } from 'react-router-dom';
import { ThemeInit } from '../.flowbite-react/init';
import { trimPrefix, trimSuffix } from './utils/strings';
import { SideNav } from './components/side-nav';
import { ChamberResponse, ListResponse } from './models/response';

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

function encodePath(path: string) {
  return path
    ? path
        .split('/')
        .map((segment) => encodeURIComponent(segment))
        .join('/')
    : path;
}

function ensureTrailingSlash(path: string) {
  return path.endsWith('/') ? path : path + '/';
}

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

const Content = () => {
  const [chamberName, setChamberName] = useState('');
  const location = useLocation();

  const { data: listResponse, isLoading: isLoadingList } = useQuery<ListResponse>(location.pathname, () => {
    return fetch(`/v1/chambers${encodePath(location.pathname)}?list=true`, {
      method: 'GET',
      mode: 'same-origin',
      headers: {
        'Content-Type': 'application/json',
      },
    }).then((res) => {
      return res.json();
    });
  });

  const { data: chamber, isLoading: isLoadingChamber } = useQuery<ChamberResponse>(
    location.pathname + '_chamber',
    () => {
      return fetch(`/v1/chambers${encodePath(location.pathname)}`, {
        method: 'GET',
        mode: 'same-origin',
        headers: {
          'Content-Type': 'application/json',
        },
      }).then((res) => {
        return res.json();
      });
    }
  );

  const { mutate } = useMutation(
    (c: string) => {
      return fetch(`/v1/chambers${encodePath(ensureTrailingSlash(location.pathname) + c)}`, {
        method: 'POST',
        body: JSON.stringify({ rules: {} }),
        mode: 'same-origin',
        headers: {
          'Content-Type': 'application/json',
        },
      }).then((res) => {
        return res.json();
      });
    },
    {
      onSettled: () => {
        return Promise.all([
          queryClient.invalidateQueries(location.pathname + '_chamber'),
          queryClient.invalidateQueries(location.pathname),
        ]);
      },
    }
  );

  const onCreateNewChamber: React.FormEventHandler<HTMLFormElement> = (event) => {
    event.preventDefault();
    mutate(chamberName);
  };

  const directories = (listResponse?.data || []).filter((curChamber) => curChamber !== '.');
  const { data: chamberData } = chamber || {};
  const { rules } = chamberData || {};
  const trimmed = trimSuffix(trimPrefix(location.pathname, '/'), '/');
  const up = trimmed !== '' ? trimmed.split('/') : [];
  const isLoading = isLoadingList || isLoadingChamber;

  return (
    <div className="flex flex-col items-center my-5">
      <h1>Realm</h1>
      <div className="w-full max-w-[1280px]">
        <Breadcrumb aria-label="directory crumbs" className="px-5 py-3">
          <BreadcrumbItem icon={HiHome}>
            <Link to="/" relative="path">
              Home
            </Link>
          </BreadcrumbItem>
          {up.map((path, index) => {
            return (
              <BreadcrumbItem key={index}>
                <Link to={'/' + [...up.slice(0, index), path].join('/')}>{path}</Link>
              </BreadcrumbItem>
            );
          })}
        </Breadcrumb>
        <div className="grid grid-cols-1 md:grid-cols-12 gap-5">
          <div className="p-3 col-span-1 md:col-span-3">
            {directories.length > 0 && <SideNav directories={directories} />}
          </div>
          <div className="grid gap-4 p-3 col-span-1 md:col-span-9">
            {isLoading && (
              <div className="text-left">
                <Spinner aria-label="Loading Chambers" size="lg" />
              </div>
            )}
            {!isLoading && (
              <form className="flex flex-col gap-4" onSubmit={onCreateNewChamber}>
                <div>
                  <div className="mb-2 block">
                    <Label htmlFor="chamber">Chamber</Label>
                  </div>
                  <TextInput
                    id="chamber"
                    type="input"
                    required
                    value={chamberName}
                    onChange={(event) => {
                      setChamberName(event.target.value);
                    }}
                  />
                </div>
                <Button type="submit">Create New Chamber</Button>
              </form>
            )}

            {!!rules && (
              <ul className="grid gap-3 list-none">
                {Object.keys(rules).map((ruleName) => {
                  const rule = rules[ruleName];
                  if (!rule) {
                    return null;
                  }
                  return (
                    <li key={ruleName}>
                      <h2>{ruleName}</h2>
                      <h3>
                        {rule.type} : {JSON.stringify(rule.value)}
                      </h3>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};
