export type ListResponse = {
  data?: Array<string>;
};

export type ChamberResponse = {
  data?: {
    rules: Rules;
  };
};

export type Rules = Record<string, Rule>;

export type Rule = {
  type: string;
  value: unknown;
};
