import { Label, TextInput } from 'flowbite-react';

export const StringRule = ({
  value,
  onChange,
  ruleName,
}: {
  value: string;
  onChange: (value: string) => void;
  ruleName: string;
}) => {
  return (
    <div>
      <Label htmlFor={ruleName}>{ruleName}</Label>
      <TextInput
        id={ruleName}
        type="text"
        sizing="md"
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
        }}
      />
    </div>
  );
};
