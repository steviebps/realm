import { useState } from 'react';
import { Breadcrumb, BreadcrumbItem, Button, Label, Spinner, TextInput } from 'flowbite-react';
import { HiHome } from 'react-icons/hi';
import { QueryClient, QueryClientProvider, useMutation, useQuery } from 'react-query';
import { BrowserRouter as Router, Routes, Route, Link, useLocation } from 'react-router-dom';
import { ThemeInit } from '../.flowbite-react/init';
import { trimPrefix, trimSuffix } from './utils/strings';
import { SideNav } from './components/side-nav';
import { ChamberResponse, ListResponse } from './models/response';
import { RuleInput } from './components/RuleInput/RuleInput';
import { encodePath, ensureTrailingSlash } from './utils/path';

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

  const { mutate, isLoading: isLoadingCreateChamber } = useMutation<null, unknown, string>(
    (c: string) => {
      return fetch(`/v1/chambers${encodePath(ensureTrailingSlash(location.pathname) + c)}`, {
        method: 'POST',
        body: JSON.stringify({
          rules: {
            string: { type: 'string', value: 'hello, world' },
            boolean: { type: 'boolean', value: true },
            number: { type: 'number', value: 10.2 },
          },
        }),
        mode: 'same-origin',
        headers: {
          'Content-Type': 'application/json',
        },
      })
        .then((res) => {
          return res.json();
        })
        .then((res: null) => {
          setChamberName('');
          return res;
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
          <div className="grid gap-8 p-3 col-span-1 md:col-span-9">
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
                {isLoadingCreateChamber && (
                  <Button type="submit" disabled>
                    <Spinner size="sm" aria-label="Creating Chammber" className="me-3" light />
                    Creating...
                  </Button>
                )}
                {!isLoadingCreateChamber && <Button type="submit">Create New Chamber</Button>}
              </form>
            )}

            {!!rules && (
              <ul className="grid gap-5 list-none">
                {Object.entries(rules).map(([ruleName, rule]) => {
                  if (!rule) {
                    return null;
                  }
                  return <RuleInput key={ruleName} ruleName={ruleName} rule={rule} />;
                })}
              </ul>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};
