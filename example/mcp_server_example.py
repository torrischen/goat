from mcp.server.fastmcp import FastMCP
from mcp.client.sse import sse_client

mcp = FastMCP(
    "TestServer",
    port=8000,
)


@mcp.tool()
def get_china_history(dynasty: str) -> str:
    """This function will help get the information of China's different dynasty"""
    return "The Tang Dynasty (618-907) was an era of Chinese history that is often regarded as a golden age of cosmopolitan culture. It was marked by significant achievements in poetry, art, and technology. The capital city, Chang'an (modern-day Xi'an), was one of the largest and most prosperous cities in the world at the time. The Tang Dynasty also saw the expansion of the Chinese empire, with influence extending to Central Asia and Korea. Notable figures from this period include the poet Li Bai and the Emperor Taizong. "

@mcp.tool()
def get_england_history() -> str:
    """This function will help get the information of England history"""
    return "The Romans stayed in Britain for almost four centuries. In some parts of the country they were met with rebellion and resistance, but in more peaceful areas cities were founded, villas constructed and a network of roads developed that can still be traced today. And in AD 122, the emperor Hadrian, visiting Britain, ordered the building of his famous wall."

mcp.run("sse")