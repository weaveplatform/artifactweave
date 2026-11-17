import org.jdom2.*;
import org.jdom2.input.*;
import org.jdom2.output.*;
import java.io.*;
public class PomRaw {
 public static void main(String[] args) throws Exception {
  SAXBuilder b = new SAXBuilder();
  b.setFeature("http://xml.org/sax/features/external-general-entities",false);
  b.setFeature("http://xml.org/sax/features/external-parameter-entities",false);
  Document d = b.build(new File(args[0]));
  Element root = d.getRootElement(); Namespace ns = root.getNamespace();
  root.removeChild("repositories",ns); root.removeChild("pluginRepositories",ns);
  Element profiles = root.getChild("profiles",ns);
  if(profiles!=null) for(Element p:profiles.getChildren()){
   Element a=p.getChild("activation",ns);
   if(args.length>1 || (a!=null && Boolean.parseBoolean(a.getChildText("activeByDefault",ns)))){
    p.removeChild("repositories",ns);p.removeChild("pluginRepositories",ns);
   }
  }
  System.out.print(new XMLOutputter().outputString(d));
 }
}
