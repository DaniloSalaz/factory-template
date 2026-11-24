import { type QueryKey, useInfiniteQuery } from "@tanstack/react-query";

/** A page of a cursor-paginated list of the API (members, invitations). */
export type CursorPage<T> = { items: T[]; next_cursor: string | null };

/**
 * useCursorPages loads a list page by page ("load more"). key is the operation's
 * generated query key, so its first element is the path: the cache keeps each tenant's
 * pages apart (query-client.ts) and invalidating the generated key reloads them.
 */
export function useCursorPages<T>(
  key: QueryKey,
  fetchPage: (cursor: string | undefined) => Promise<CursorPage<T>>,
) {
  const query = useInfiniteQuery({
    queryKey: [...key, "pages"],
    queryFn: ({ pageParam }) => fetchPage(pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
  const items = query.data?.pages.flatMap((p) => p.items) ?? [];
  return { ...query, items };
}
