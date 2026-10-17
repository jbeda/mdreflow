import {
  Tabs,
  TabItem,
} from '@site/components'

export const meta = {
  title: 'Statements over several lines',
  tags: ['mdx', 'esm'],
}

export function Example({ name }) {
  const greeting = `Hello, ${name}`

  return (
    <p>{greeting}</p>
  )
}

export const Box = styled.div`
  width: 100%;
  padding: 1rem;
`

The statements above are JavaScript. This paragraph is prose and
still reflows.

export default ({ children }) => <Layout>{children}</Layout>
