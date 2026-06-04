import { useState, useEffect } from "react";
import { useAuth } from "../lib/auth";
import { AdminHosts } from "./admin/AdminHosts";
import { AdminMachines } from "./admin/AdminMachines";
import { AdminActivity } from "./admin/AdminActivity";
import { AdminLogs } from "./admin/AdminLogs";
import { AdminEvents } from "./admin/AdminEvents";

const tabs = [
  { id: "hosts", label: "Hosts" },
  { id: "machines", label: "Machines" },
  { id: "activity", label: "Activity" },
  { id: "events", label: "Events" },
  { id: "logs", label: "Logs" },
] as const;

type TabId = (typeof tabs)[number]["id"];

function getTabFromHash(): TabId {
  const hash = window.location.hash.slice(1);
  if (tabs.some((t) => t.id === hash)) return hash as TabId;
  return "hosts";
}

export function Admin() {
  const { isAdmin } = useAuth();
  const [activeTab, setActiveTab] = useState<TabId>(getTabFromHash);

  useEffect(() => {
    const onHashChange = () => setActiveTab(getTabFromHash());
    window.addEventListener("hashchange", onHashChange);
    return () => window.removeEventListener("hashchange", onHashChange);
  }, []);

  const switchTab = (id: TabId) => {
    window.location.hash = id;
    setActiveTab(id);
  };

  if (!isAdmin) {
    return (
      <p className="text-gray-500 dark:text-gray-400 py-16 text-center">
        You do not have access to this page.
      </p>
    );
  }

  return (
    <div>
      <nav className="flex gap-1 border-b border-gray-200 dark:border-gray-700 mb-6">
        {tabs.map((tab) => (
          <button
            key={tab.id}
            onClick={() => switchTab(tab.id)}
            className={`px-4 py-2.5 text-sm font-medium border-b-2 transition-colors ${
              activeTab === tab.id
                ? "border-brand-600 text-brand-600 dark:text-brand-400"
                : "border-transparent text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-300"
            }`}
          >
            {tab.label}
          </button>
        ))}
      </nav>
      {activeTab === "hosts" && <AdminHosts />}
      {activeTab === "machines" && <AdminMachines />}
      {activeTab === "activity" && <AdminActivity />}
      {activeTab === "events" && <AdminEvents />}
      {activeTab === "logs" && <AdminLogs />}
    </div>
  );
}
