import { z } from 'zod';

/** Create/Edit backend form. Name is required on create, fixed on edit. */
export const backendFormSchema = z.object({
  name: z.string().min(1),
  routingGroup: z.string().min(1),
  proxyTo: z.string().min(1),
  externalUrl: z.string().min(1),
  active: z.boolean(),
});

export type BackendFormValues = z.infer<typeof backendFormSchema>;
