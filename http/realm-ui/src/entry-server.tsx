import ReactDOMServer from 'react-dom/server';
import { App } from './App';
import './index.css';

export function render() {
  return ReactDOMServer.renderToString(<App />);
}
