export function Button({ label, onClick }) {
  return <button onClick={onClick}>{label}</button>;
}

export const Panel = ({ children }) => <section>{children}</section>;
