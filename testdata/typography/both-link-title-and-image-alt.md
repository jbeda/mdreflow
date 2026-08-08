# Link titles and image alt text

A link's destination and title are both immune, even though the title text looks like ordinary prose: [the docs](https://example.com/a "isn't this neat...") stays as written.

An image's alt text is treated the same way as link text: ![a "diagram" of the flow...](img/a.png "the diagram's title") stays as written too.

An autolink's URL can contain a run of dots without becoming an ellipsis: <https://example.com/a...b> stays byte-for-byte.

Ordinary prose around all of this still gets curled: "like this..." right here.
