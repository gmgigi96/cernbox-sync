import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ServerSelector } from "../components/ServerSelector";
import type { Folder } from "../types";

const PROD = "https://cernbox.cern.ch";
const QA = "https://qa.cernbox.cern.ch";
const PROD_FOLDER = { Name: "Documents", RemoteBase: `${PROD}/remote.php/dav/spaces/1` } as Folder;

function setup(folders: Folder[] = []) {
  const onSelect = vi.fn();
  render(<ServerSelector serverUrl={PROD} folders={folders} confirmLabel="Use this server" onSelect={onSelect} />);
  fireEvent.click(screen.getByRole("button", { name: "Change server" }));
  return onSelect;
}

const confirm = () => screen.getByRole("button", { name: "Use this server" });
const choose = (value: string) => fireEvent.change(screen.getByLabelText("Server"), { target: { value } });

describe("ServerSelector", () => {
  it("shows the current server until asked to change it", () => {
    render(<ServerSelector serverUrl={QA} folders={[]} confirmLabel="Use this server" onSelect={vi.fn()} />);
    expect(screen.getByText("CERNBox QA")).toBeInTheDocument();
    expect(screen.queryByLabelText("Server")).not.toBeInTheDocument();
  });

  it("selects a listed server, but not the current one", () => {
    const onSelect = setup();
    expect(confirm()).toBeDisabled();

    choose(QA);
    fireEvent.click(confirm());

    expect(onSelect).toHaveBeenCalledWith(QA);
  });

  it("only accepts a valid custom URL", () => {
    const onSelect = setup();
    choose("custom");

    fireEvent.change(screen.getByLabelText("Server URL"), { target: { value: "example.org" } });
    expect(screen.getByText(/Enter a full URL/)).toBeInTheDocument();
    expect(confirm()).toBeDisabled();

    fireEvent.change(screen.getByLabelText("Server URL"), { target: { value: "https://example.org/" } });
    fireEvent.click(confirm());

    expect(onSelect).toHaveBeenCalledWith("https://example.org");
  });

  it("opens on the custom URL for a server that is not listed", () => {
    render(<ServerSelector serverUrl="http://localhost" folders={[]} confirmLabel="Use this server" onSelect={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Change server" }));

    expect(screen.getByLabelText("Server URL")).toHaveValue("http://localhost");
    expect(screen.queryByRole("option", { name: "localhost" })).not.toBeInTheDocument();
  });

  it("warns about folders on another server", () => {
    setup([PROD_FOLDER]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    choose(QA);

    expect(screen.getByRole("alert")).toHaveTextContent("Documents");
  });
});
