import { useEffect, useState } from 'react';
import { Label, TextInput, Button } from 'flowbite-react';
import { ChamberResponse, Rule } from '../../models/response';
import { useMutation, useQueryClient } from 'react-query';
import { encodePath, ensureTrailingSlash } from '../../utils/path';
import { useLocation } from 'react-router-dom';
import { BooleanRule } from '../BooleanRule/BooleanRule';

const determineValue = (type: Rule['type'], value: string): Rule => {
  switch (type) {
    case 'string':
      return { type: 'string', value: value };
    case 'number':
      return { type: 'number', value: Number(value) };
    case 'boolean':
      return { type: 'boolean', value: value.toLowerCase() === 'true' };
    case 'custom':
      return { type: 'custom', value: {} };
  }
};

export const RuleInput = ({ ruleName, rule }: { ruleName: string; rule: Rule }) => {
  const [value, setValue] = useState(JSON.stringify(rule.value));
  const location = useLocation();
  const queryClient = useQueryClient();
  const res = queryClient.getQueryData<ChamberResponse>(location.pathname + '_chamber');

  useEffect(() => {
    setValue(JSON.stringify(rule.value));
  }, [rule.value]);

  const { mutate } = useMutation<null, unknown, Rule>(
    (r: Rule) => {
      if (!res?.data) {
        return Promise.reject(new Error('could not retrieve chamber'));
      }
      return fetch(`/v1/chambers${encodePath(ensureTrailingSlash(location.pathname))}`, {
        method: 'PATCH',
        body: JSON.stringify({
          ...res.data,
          rules: {
            [ruleName]: r,
          },
        }),
        mode: 'same-origin',
        headers: {
          'Content-Type': 'application/json',
        },
      })
        .then((res) => {
          return res.json();
        })
        .then((res: null) => {
          return res;
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

  const onUpdateRule: React.FormEventHandler<HTMLFormElement> = (event) => {
    event.preventDefault();
    mutate(determineValue(rule.type, value));
  };

  return (
    <li className="flex flex-col gap-3">
      <form onSubmit={onUpdateRule}>
        <div className="grid items-end gap-5 grid-cols-2">
          {rule.type === 'boolean' && (
            <BooleanRule onChange={(value) => setValue(value)} value={value.toLowerCase() === 'true'} />
          )}
          {rule.type === 'string' && (
            <div>
              <Label htmlFor={ruleName}>{ruleName}</Label>
              <TextInput
                id={ruleName}
                type="text"
                sizing="md"
                value={value}
                onChange={(event) => {
                  setValue(event.target.value);
                }}
              />
            </div>
          )}
          <Button type="submit" size="md" color="alternative">
            Update
          </Button>
        </div>
      </form>
    </li>
  );
};
