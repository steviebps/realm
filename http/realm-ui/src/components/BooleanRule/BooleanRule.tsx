import { Label, Radio } from 'flowbite-react';

export const BooleanRule = ({ value, onChange }: { value: boolean; onChange: (value: string) => void }) => {
  return (
    <div className="flex flex-row gap-3">
      <div className="flex items-center gap-2">
        <Radio id="true-radio" name="true" value="true" checked={value} onChange={() => onChange('true')} />
        <Label htmlFor="true-radio">True</Label>
      </div>
      <div className="flex items-center gap-2">
        <Radio id="false-radio" name="false" value="false" checked={!value} onChange={() => onChange('false')} />
        <Label htmlFor="false-radio">False</Label>
      </div>
    </div>
  );
};
