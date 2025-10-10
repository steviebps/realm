import { Sidebar, SidebarItem, SidebarItemGroup, SidebarItems } from 'flowbite-react';
import { HiFolder } from 'react-icons/hi';
import { useHref } from 'react-router-dom';

export const SideNav = ({ directories }: { directories: string[] }) => {
  return (
    <Sidebar aria-label="Chamber list">
      <SidebarItems>
        <SidebarItemGroup>
          {directories.map((curChamber) => {
            return <Item key={curChamber} directory={curChamber} />;
          })}
        </SidebarItemGroup>
      </SidebarItems>
    </Sidebar>
  );
};

const Item = ({ directory }: { directory: string }) => {
  const href = useHref(directory, { relative: 'path' });
  return (
    <SidebarItem key={directory} href={href} icon={HiFolder}>
      {directory}
    </SidebarItem>
  );
};
