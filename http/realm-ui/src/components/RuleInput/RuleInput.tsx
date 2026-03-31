import { useEffect, useState } from 'react';
import { useLocation } from 'react-router-dom';
import { Label, TextInput, Button } from 'flowbite-react';
import { HiTrash } from 'react-icons/hi';
import { ChamberResponse, Rule } from '../../models/response';
import { useMutation, useQueryClient } from 'react-query';
import { encodePath, ensureTrailingSlash } from '../../utils/path';
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

  const { mutate: deleteRule } = useMutation<null, unknown, string>(
    (ruleName: string) => {
      if (!res?.data) {
        return Promise.reject(new Error('could not retrieve chamber'));
      }

      const rules = { ...res.data.rules };
      delete rules[ruleName];
      return fetch(`/v1/chambers${encodePath(ensureTrailingSlash(location.pathname))}`, {
        method: 'POST',
        body: JSON.stringify({
          ...res.data,
          rules,
        }),
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

  const onUpdateRule: React.FormEventHandler<HTMLFormElement> = (event) => {
    event.preventDefault();
    mutate(determineValue(rule.type, value));
  };

  const onDelete: React.MouseEventHandler<HTMLButtonElement> = () => {
    deleteRule(ruleName);
  };

  return (
    <li className="flex flex-col gap-3">
      <form onSubmit={onUpdateRule}>
        <div className="grid items-end gap-5 grid-cols-2">
          {rule.type === 'number' && (
            <div>
              <Label htmlFor={ruleName}>{ruleName}</Label>
              <TextInput
                id={ruleName}
                type="number"
                sizing="md"
                value={value}
                onChange={(event) => {
                  setValue(event.target.value);
                }}
              />
            </div>
          )}
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
          <div className="grid gap-2 grid-cols-2">
            <Button type="submit" size="md" color="default">
              Update
            </Button>
            <Button type="button" size="md" color="red" outline onClick={onDelete}>
              Delete
              <HiTrash />
            </Button>
          </div>
        </div>
      </form>
    </li>
  );
};
