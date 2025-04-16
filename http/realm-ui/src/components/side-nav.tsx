import { Sidebar } from 'flowbite-react';
import { HiFolder } from 'react-icons/hi';
import { useHref } from 'react-router-dom';

export const SideNav = ({ directories }: { directories: string[] }) => {
  return (
    <Sidebar aria-label="Chamber list">
      <Sidebar.Items>
        <Sidebar.ItemGroup>
          {directories.map((curChamber) => {
            return <Item key={curChamber} directory={curChamber} />;
          })}
        </Sidebar.ItemGroup>
      </Sidebar.Items>
    </Sidebar>
  );
};

const Item = ({ directory }: { directory: string }) => {
  const href = useHref(directory, { relative: 'path' });
  return (
    <Sidebar.Item key={directory} href={href} icon={HiFolder}>
      {directory}
    </Sidebar.Item>
  );
};
