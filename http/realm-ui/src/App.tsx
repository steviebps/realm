import { useState } from 'react';
import { Breadcrumb, Button, Label, TextInput } from 'flowbite-react';
import { HiHome } from 'react-icons/hi';
import { QueryClient, QueryClientProvider, useMutation, useQuery } from 'react-query';
import { BrowserRouter as Router, Routes, Route, Link, useLocation } from 'react-router-dom';
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

export const App = () => {
  return (
    <QueryClientProvider client={queryClient}>
      <Router
        basename="/ui"
        future={{
          v7_relativeSplatPath: true,
        }}
      >
        <Routes>
          <Route path="*" element={<Content />} />
        </Routes>
      </Router>
    </QueryClientProvider>
  );
};

const Content = () => {
  const [chamberName, setChamberName] = useState('');
  const location = useLocation();

  const { data: listResponse } = useQuery<ListResponse>(location.pathname, () => {
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
      return fetch(`/v1/chambers${encodePath(location.pathname)}${encodeURI(c)}`, {
        method: 'POST',
        body: testData,
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

  return (
    <div className="flex flex-col items-center my-5">
      <h1>Realm</h1>
      <div className="w-[1280px]">
        <Breadcrumb aria-label="directory crumbs" className="px-5 py-3">
          <Breadcrumb.Item icon={HiHome}>
            <Link to="/" relative="path">
              Home
            </Link>
          </Breadcrumb.Item>
          {up.map((path, index) => {
            return (
              <Breadcrumb.Item key={index}>
                <Link to={'/' + [...up.slice(0, index), path].join('/')}>{path}</Link>
              </Breadcrumb.Item>
            );
          })}
        </Breadcrumb>
        <div className="grid grid-cols-12 gap-5">
          <div className="col-span-3">{directories.length > 0 && <SideNav directories={directories} />}</div>
          <div className="col-span-9">
            {!isLoadingChamber && !rules && (
              <form className="flex max-w-md flex-col gap-4" onSubmit={onCreateNewChamber}>
                <div>
                  <div className="mb-2 block">
                    <Label htmlFor="chamber" value="Chamber" />
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
                        {rule.type} : {String(rule.value)}
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

const testData = `{  "rules": {
  "feature switch one": {
    "type": "number",
    "value": 10.6,
    "overrides": [
      {
        "type": "number",
        "value": 10.2,
        "minimumVersion": "v0.0.1",
        "maximumVersion": "v2.0.0"
      },
      {
        "type": "number",
        "value": 10.4,
        "minimumVersion": "v2.0.0",
        "maximumVersion": "v3.0.0"
      },
      {
        "type": "number",
        "value": 10.6,
        "minimumVersion": "v3.0.0",
        "maximumVersion": "v4.0.0"
      },
      {
        "type": "number",
        "value": 10.8,
        "minimumVersion": "v4.0.0",
        "maximumVersion": "v5.0.0"
      },
      {
        "type": "number",
        "value": 11.0,
        "minimumVersion": "v5.0.0",
        "maximumVersion": "v6.0.0"
      }
    ]
  }
}
}`;
