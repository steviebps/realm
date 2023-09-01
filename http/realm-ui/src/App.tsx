// import { useEffect, useState } from 'react';
import { QueryClient, QueryClientProvider, useQuery } from 'react-query';
import { BrowserRouter as Router, Routes, Route, Link, useLocation } from 'react-router-dom';

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
  value: any;
};

const Content = () => {
  const location = useLocation();

  const { data } = useQuery<ListResponse>(location.pathname, () => {
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

  const { data: chamber } = useQuery<ChamberResponse>(location.pathname + '_chamber', () => {
    return fetch(`/v1/chambers${encodePath(location.pathname)}`, {
      method: 'GET',
      mode: 'same-origin',
      headers: {
        'Content-Type': 'application/json',
      },
    }).then((res) => {
      return res.json();
    });
  });

  const { data: chamberData } = chamber || {};
  const { toggles } = chamberData || {};

  return (
    <div>
      <h1>Realm</h1>
      <div className="grid gap-3">
        <ul>
          {data?.data?.map((curChamber) => {
            const to = curChamber === '.' ? '../' : curChamber;
            return (
              <li key={curChamber}>
                <Link to={to} relative="path">
                  {curChamber}
                </Link>
              </li>
            );
          })}
        </ul>

        {!!toggles && (
          <ul className="grid gap-3 list-none">
            {Object.keys(toggles).map((toggleName) => {
              const toggle = toggles[toggleName];
              if (!toggle) {
                return null;
              }
              return (
                <li>
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
