import { useState } from 'react';
import { Breadcrumb, Button, Label, TextInput } from 'flowbite-react';
import { QueryClient, QueryClientProvider, useMutation, useQuery } from 'react-query';
import { BrowserRouter as Router, Routes, Route, Link, useLocation } from 'react-router-dom';
import { trimPrefix, trimSuffix } from './utils/strings';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      refetchOnMount: false,
      refetchOnReconnect: false,
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
      <Router basename="/ui">
        <Routes>
          <Route path="*" element={<Content />}></Route>
        </Routes>
      </Router>
    </QueryClientProvider>
  );
};

type ListResponse = {
  data?: Array<string>;
};

type ChamberResponse = {
  data?: {
    toggles: Toggles;
  };
};

type Toggles = Record<string, Toggle>;

type Toggle = {
  type: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  value: any;
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

  const { data: chamberData } = chamber || {};
  const { toggles } = chamberData || {};

  const trimmed = trimSuffix(trimPrefix(location.pathname, '/'), '/');
  const up = trimmed !== '' ? trimmed.split('/') : [];
  return (
    <div>
      <h1>Realm</h1>
      <Breadcrumb aria-label="chamber crumbs" className="bg-gray-50 px-5 py-3 dark:bg-gray-800">
        <Breadcrumb.Item href="#">
          <Link to="/" relative="path">
            Home
          </Link>
        </Breadcrumb.Item>
        {up.map((path, index) => (
          <Breadcrumb.Item key={index} href="">
            <Link to={up.slice(0, index).join('/') + '/' + path} relative="route">
              {path}
            </Link>
          </Breadcrumb.Item>
        ))}
      </Breadcrumb>
      <div className="grid gap-5">
        <ul>
          {listResponse?.data
            ?.filter((curChamber) => curChamber !== '.')
            .map((curChamber) => {
              return (
                <li key={curChamber}>
                  <Link to={curChamber} relative="path">
                    {curChamber}
                  </Link>
                </li>
              );
            })}
        </ul>

        {!isLoadingChamber && !toggles && (
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

        {!!toggles && (
          <ul className="grid gap-3 list-none">
            {Object.keys(toggles).map((toggleName) => {
              const toggle = toggles[toggleName];
              if (!toggle) {
                return null;
              }
              return (
                <li key={toggleName}>
                  <h2>{toggleName}</h2>
                  <h3>
                    {toggle.type} : {String(toggle.value)}
                  </h3>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
};

const testData = `{  "toggles": {
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
