import { Link } from 'react-router-dom';
import { trimSuffix } from '../utils/strings';

/** The chambers directly beneath the current one, drawn as branches of a tree. */
export const SideNav = ({ directories }: { directories: string[] }) => {
  return (
    <nav aria-label="Child chambers">
      <h2 className="mb-2 text-sm font-semibold text-gray-600 dark:text-gray-400">Child chambers</h2>
      <ul className="ml-1.5 border-l border-gray-300 dark:border-gray-600">
        {directories.map((directory) => (
          <li key={directory}>
            <Link
              to={directory}
              relative="path"
              className="group flex items-center gap-2 py-1 pr-2 font-mono text-sm text-gray-800 hover:text-primary-700 focus-visible:outline-2 focus-visible:outline-primary-500 dark:text-gray-200 dark:hover:text-primary-300"
            >
              <span aria-hidden="true" className="h-px w-3 bg-gray-300 dark:bg-gray-600" />
              <span className="truncate rounded px-1.5 py-0.5 group-hover:bg-primary-50 dark:group-hover:bg-primary-900/40">
                {trimSuffix(directory, '/')}
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
};
