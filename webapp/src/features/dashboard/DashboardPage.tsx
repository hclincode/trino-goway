import { Col, Row } from 'antd';
import { useDistribution } from './queries';
import { SummaryCard } from './SummaryCard';
import { LineChartCard } from './LineChartCard';
import { DoughnutChartCard } from './DoughnutChartCard';

/** Gateway health + query-throughput overview. */
export default function DashboardPage() {
  const { data } = useDistribution();

  return (
    <Row gutter={[16, 16]}>
      <Col xs={24}>
        <SummaryCard detail={data} />
      </Col>
      <Col xs={24} lg={16}>
        <LineChartCard detail={data} />
      </Col>
      <Col xs={24} lg={8}>
        <DoughnutChartCard detail={data} />
      </Col>
    </Row>
  );
}
