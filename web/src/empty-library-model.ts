export type LibraryContentStatus = { library_empty?: boolean; has_sources?: boolean; busy?: boolean };
export type EmptyLibraryState = { message: string; action?: { label: string; href: string } };

/** Only a confirmed empty catalog replaces a page's own results or errors.
 * Say conversations are arriving only while a capture or index runs. */
export function emptyLibraryState(status: LibraryContentStatus | null): EmptyLibraryState | null {
  if (!status?.library_empty) return null;
  if (status.busy) return { message: "Pulling in conversations - this may take some time" };
  return status.has_sources
    ? { message: "No conversations indexed yet", action: { label: "Review sources", href: "/settings/sources" } }
    : { message: "No sources configured yet", action: { label: "Configure", href: "/settings/sources" } };
}
