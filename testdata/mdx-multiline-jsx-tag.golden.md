# Multi-line JSX opening tags

A code sample passed to a component as a template literal can hold blank lines.
The text after each blank line is still part of the attribute.

<Example
  title="Deposit + delegate to a session key"
  sdk={`import { ephemeralKey } from "@sugar-rush/sdk";

const session = await ephemeralKey();          // a throwaway trading key
localStorage.setItem("sr-session", session.export());

await client.deposit({
  amountAda: 15,
  onDone: (tx) => console.log("deposited", tx), // -> a tx id
});
// Reconnect as \`session\` and trade; your wallet key never signs an order.`}
  py={`from sugar_rush import ephemeral_key

session = ephemeral_key()          # a throwaway trading key`}
/>

This paragraph follows the element.
It is ordinary prose and still reflows.

<Note
  title={`One line.

Another line, after a blank one.`}>

The children of a paired element are prose.
They reflow as usual.

</Note>

<Tip
  label="closes in its own paragraph">
Text that shares the tag's paragraph stays as it is.
</Tip>
