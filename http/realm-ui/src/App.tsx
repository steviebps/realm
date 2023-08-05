import { QueryClient, QueryClientProvider, useQuery } from "react-query";

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

export const App = () => {
  return (<QueryClientProvider client={queryClient}><Content /></QueryClientProvider>)
}

const Content = () => {
  const { data } = useQuery('root', () => {
    return fetch('/v1/chambers/', {
      method: 'GET',
      mode: 'same-origin',
      headers: {
        "Content-Type": "application/json",
      },
      body: null,
    }).then((res) => {
      return res.json();
    })
  });

  return (
    <>
      <div>
        <h1>Realm</h1>
      </div>
    </>
  )
};
